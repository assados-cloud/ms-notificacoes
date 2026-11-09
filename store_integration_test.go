package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Banco descartável exclusivo de testes; não apontar para o banco da equipe.
func TestDatabaseIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL não configurada")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(ctx, schema); err != nil {
		t.Fatal(err)
	}
	const id = int64(9223372036854775000)
	if _, err = db.Exec(ctx, "DELETE FROM notificacoes WHERE pedido_id=$1", id); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(ctx, "DELETE FROM notificacoes WHERE pedido_id=$1", id)
	for i := 0; i < 2; i++ {
		r, err := db.Exec(ctx, `INSERT INTO notificacoes(pedido_id,cliente_nome,cliente_email) VALUES($1,'Teste','teste@example.com') ON CONFLICT(pedido_id) DO NOTHING`, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.RowsAffected() != int64(1-i) {
			t.Fatal("duplicidade não suprimida")
		}
	}
	calls := 0
	failed := func(Pedido) error { calls++; return errors.New("SMTP fora do ar") }
	for attempt := 1; attempt <= 5; attempt++ {
		if _, err = db.Exec(ctx, "UPDATE notificacoes SET proxima_tentativa=now() WHERE pedido_id=$1", id); err != nil {
			t.Fatal(err)
		}
		if found, err := deliverOne(ctx, db, failed); err != nil || !found {
			t.Fatalf("processamento: %v %v", found, err)
		}
	}
	if calls != 5 {
		t.Fatal(calls)
	}
	if found, err := deliverOne(ctx, db, failed); err != nil || found {
		t.Fatal("tentativa após limite", found, err)
	}
	var status string
	if err = db.QueryRow(ctx, "SELECT status FROM notificacoes WHERE pedido_id=$1", id).Scan(&status); err != nil || status != "FALHOU" {
		t.Fatal(status, err)
	}
	if _, err = db.Exec(ctx, "UPDATE notificacoes SET status='PENDENTE',tentativas=0 WHERE pedido_id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err = deliverOne(ctx, db, func(Pedido) error { return nil }); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	api(db).ServeHTTP(response, httptest.NewRequest("GET", "/notificacoes/9223372036854775000", nil))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if err = db.QueryRow(ctx, "SELECT status FROM notificacoes WHERE pedido_id=$1", id).Scan(&status); err != nil || status != "ENVIADO" {
		t.Fatal(status, err)
	}
}
