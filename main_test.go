package main

import (
	"testing"
	"time"
)

func TestPedidoContract(t *testing.T) {
	valid := `{"pedido_id":12,"cliente_nome":"Maria","cliente_email":"maria@example.com","itens":[{"produto_id":1,"quantidade":2}],"total":57,"criado_em":"2026-11-10T23:30:00Z"}`
	p, err := parsePedido([]byte(valid))
	if err != nil || p.PedidoID != 12 {
		t.Fatalf("contrato de Pedidos rejeitado: %v", err)
	}
	for _, body := range []string{
		`{"pedido_id":"12","cliente_nome":"Maria","cliente_email":"maria@example.com"}`,
		`{"pedido_id":0,"cliente_nome":"Maria","cliente_email":"maria@example.com"}`,
		`{"pedido_id":12,"cliente_nome":"Maria"}`,
		`{"pedido_id":12,"cliente_nome":"Maria","cliente_email":"Maria <maria@example.com>"}`,
		`{"pedido_id":12,"cliente_nome":"Maria","cliente_email":"a@example.com\r\nBcc: b@example.com"}`,
		`null`, `{} VALID`,
	} {
		if _, err := parsePedido([]byte(body)); err == nil {
			t.Errorf("evento inválido aceito: %s", body)
		}
	}
}

func TestRetryPolicy(t *testing.T) {
	for i, want := range []time.Duration{5, 10, 20, 40} {
		status, delay := retryStatus(i+1, false)
		if status != "PENDENTE" || delay != want*time.Second {
			t.Fatalf("tentativa %d: %s %v", i+1, status, delay)
		}
	}
	status, _ := retryStatus(5, false)
	if status != "FALHOU" {
		t.Fatal(status)
	}
	status, _ = retryStatus(5, true)
	if status != "ENVIADO" {
		t.Fatal(status)
	}
}

func TestSMTPRejectsUnsafeConfiguration(t *testing.T) {
	t.Setenv("SMTP_FROM", "pedidos@example.com")
	t.Setenv("SMTP_TLS", "none")
	t.Setenv("SMTP_USER", "user")
	t.Setenv("SMTP_PASSWORD", "secret")
	if _, err := newSMTPSender(); err == nil {
		t.Fatal("credenciais sem TLS aceitas")
	}
	t.Setenv("SMTP_TLS", "starttls")
	t.Setenv("SMTP_FROM", "a@example.com\r\nBcc: b@example.com")
	if _, err := newSMTPSender(); err == nil {
		t.Fatal("injeção de header aceita")
	}
}
