package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errNoRows = pgx.ErrNoRows

const schema = `CREATE TABLE IF NOT EXISTS notificacoes (
 pedido_id BIGINT PRIMARY KEY CHECK (pedido_id > 0),
 cliente_nome TEXT NOT NULL,
 cliente_email TEXT NOT NULL,
 status TEXT NOT NULL DEFAULT 'PENDENTE' CHECK (status IN ('PENDENTE','ENVIADO','FALHOU')),
 tentativas INTEGER NOT NULL DEFAULT 0,
 proxima_tentativa TIMESTAMPTZ NOT NULL DEFAULT now(),
 criado_em TIMESTAMPTZ NOT NULL DEFAULT now(),
 enviado_em TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS notificacoes_pendentes ON notificacoes(proxima_tentativa) WHERE status='PENDENTE';`

func retryStatus(attempt int, success bool) (string, time.Duration) {
	if success {
		return "ENVIADO", 0
	}
	if attempt >= 5 {
		return "FALHOU", 0
	}
	return "PENDENTE", time.Duration(1<<uint(attempt-1)) * 5 * time.Second
}

// A trava impede envio concorrente do mesmo registro por duas réplicas.
// SMTP não participa da transação: há possibilidade de duplicidade se o processo
// morrer após aceitação pelo servidor SMTP e antes do commit (at-least-once).
func deliverOne(ctx context.Context, db *pgxpool.Pool, send func(Pedido) error) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	var p Pedido
	var attempts int
	err = tx.QueryRow(ctx, `SELECT pedido_id,cliente_nome,cliente_email,tentativas FROM notificacoes WHERE status='PENDENTE' AND proxima_tentativa<=now() ORDER BY proxima_tentativa FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&p.PedidoID, &p.Nome, &p.Email, &attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	attempts++
	sendErr := send(p)
	status, delay := retryStatus(attempts, sendErr == nil)
	_, err = tx.Exec(ctx, `UPDATE notificacoes SET status=$2,tentativas=$3,proxima_tentativa=now()+($4 * interval '1 second'),enviado_em=CASE WHEN $2='ENVIADO' THEN now() ELSE NULL END WHERE pedido_id=$1`, p.PedidoID, status, attempts, delay.Seconds())
	if err != nil {
		return true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	slog.Info("notificação processada", "pedido_id", p.PedidoID, "status", status, "tentativas", attempts)
	return true, nil
}
