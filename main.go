package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	amqp "github.com/rabbitmq/amqp091-go"
)

const exchange = "pedido.criado"

type Pedido struct {
	PedidoID int64  `json:"pedido_id"`
	Nome     string `json:"cliente_nome"`
	Email    string `json:"cliente_email"`
}

func parsePedido(body []byte) (Pedido, error) {
	var p Pedido
	if len(body) > 100*1024 {
		return p, errors.New("evento muito grande")
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return p, err
	}
	if p.PedidoID <= 0 || strings.TrimSpace(p.Nome) == "" || len(p.Nome) > 200 {
		return p, errors.New("pedido_id ou cliente_nome inválido")
	}
	a, err := mail.ParseAddress(p.Email)
	if err != nil || a.Address != p.Email || strings.ContainsAny(p.Email, "\r\n") || len(p.Email) > 254 {
		return p, errors.New("cliente_email inválido")
	}
	return p, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func pause(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func consume(ctx context.Context, db *pgxpool.Pool, url string) error {
	conn, err := amqp.DialConfig(url, amqp.Config{Dial: amqp.DefaultDial(5 * time.Second), Heartbeat: 10 * time.Second})
	if err != nil {
		return err
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return err
	}
	defer ch.Close()
	if err = ch.ExchangeDeclare(exchange, "fanout", true, false, false, false, nil); err != nil {
		return err
	}
	if err = ch.ExchangeDeclare("notificacoes.dlx", "fanout", true, false, false, false, nil); err != nil {
		return err
	}
	if _, err = ch.QueueDeclare("notificacoes.invalidas", true, false, false, false, nil); err != nil {
		return err
	}
	if err = ch.QueueBind("notificacoes.invalidas", "", "notificacoes.dlx", false, nil); err != nil {
		return err
	}
	if _, err = ch.QueueDeclare("notificacoes", true, false, false, false, amqp.Table{"x-dead-letter-exchange": "notificacoes.dlx"}); err != nil {
		return err
	}
	if err = ch.QueueBind("notificacoes", "", exchange, false, nil); err != nil {
		return err
	}
	if err = ch.Qos(1, 0, false); err != nil {
		return err
	}
	msgs, err := ch.Consume("notificacoes", "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	slog.Info("consumidor pronto", "fila", "notificacoes")
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-msgs:
			if !ok {
				return errors.New("canal RabbitMQ fechado")
			}
			p, err := parsePedido(msg.Body)
			if err != nil {
				slog.Warn("evento inválido encaminhado para notificacoes.invalidas")
				if err = msg.Reject(false); err != nil {
					return err
				}
				continue
			}
			queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			result, err := db.Exec(queryCtx, `INSERT INTO notificacoes(pedido_id,cliente_nome,cliente_email) VALUES($1,$2,$3) ON CONFLICT(pedido_id) DO NOTHING`, p.PedidoID, p.Nome, p.Email)
			cancel()
			if err != nil {
				return errors.New("falha ao persistir evento; mensagem será reentregue")
			}
			if err = msg.Ack(false); err != nil {
				return err
			}
			slog.Info("evento persistido", "pedido_id", p.PedidoID, "novo", result.RowsAffected() == 1)
		}
	}
}

func api(db *pgxpool.Pool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /notificacoes/live", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /notificacoes/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			writeJSON(w, 503, map[string]string{"status": "indisponivel"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok", "postgres": "ok"})
	})
	mux.HandleFunc("GET /notificacoes/{pedido_id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("pedido_id"), 10, 64)
		if err != nil || id <= 0 {
			writeJSON(w, 400, map[string]string{"erro": "pedido_id inválido"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		var status string
		var attempts int
		var created time.Time
		var sent *time.Time
		err = db.QueryRow(ctx, `SELECT status,tentativas,criado_em,enviado_em FROM notificacoes WHERE pedido_id=$1`, id).Scan(&status, &attempts, &created, &sent)
		if err != nil {
			if errors.Is(err, errNoRows) {
				writeJSON(w, 404, map[string]string{"erro": "notificação não encontrada"})
			} else {
				writeJSON(w, 503, map[string]string{"erro": "banco indisponível"})
			}
			return
		}
		writeJSON(w, 200, map[string]any{"pedido_id": id, "status": status, "tentativas": attempts, "criado_em": created, "enviado_em": sent})
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for _, k := range []string{"DATABASE_URL", "RABBITMQ_URL", "SMTP_HOST", "SMTP_FROM"} {
		if os.Getenv(k) == "" {
			return fmt.Errorf("variável obrigatória: %s", k)
		}
	}
	sender, err := newSMTPSender()
	if err != nil {
		return err
	}
	db, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return errors.New("DATABASE_URL inválida")
	}
	defer db.Close()
	for {
		qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = db.Exec(qctx, schema)
		cancel()
		if err == nil {
			break
		}
		slog.Warn("aguardando PostgreSQL")
		if !pause(ctx, 5*time.Second) {
			return nil
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			if err := consume(ctx, db, os.Getenv("RABBITMQ_URL")); err != nil {
				slog.Warn("consumidor desconectado; reconectando")
			}
			if !pause(ctx, 5*time.Second) {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			found, err := deliverOne(ctx, db, sender.Send)
			if err != nil {
				slog.Error("falha no processamento da notificação")
			}
			if !found || err != nil {
				if !pause(ctx, time.Second) {
					return
				}
			}
		}
	}()
	srv := &http.Server{Addr: ":" + env("PORT", "8080"), Handler: api(db), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	errs := make(chan error, 1)
	go func() { slog.Info("API pronta", "addr", srv.Addr); errs <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case err = <-errs:
		stop()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	wg.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("inicialização falhou", "erro", err.Error())
		os.Exit(1)
	}
}
