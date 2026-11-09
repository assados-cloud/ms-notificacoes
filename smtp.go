package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"
)

type smtpSender struct{ host, port, from, user, password, mode string }

func newSMTPSender() (smtpSender, error) {
	s := smtpSender{host: os.Getenv("SMTP_HOST"), port: env("SMTP_PORT", "587"), from: os.Getenv("SMTP_FROM"), user: os.Getenv("SMTP_USER"), password: os.Getenv("SMTP_PASSWORD"), mode: env("SMTP_TLS", "starttls")}
	a, err := mail.ParseAddress(s.from)
	if err != nil || a.Address != s.from || strings.ContainsAny(s.from, "\r\n") {
		return s, fmt.Errorf("SMTP_FROM inválido")
	}
	if s.mode != "starttls" && s.mode != "tls" && s.mode != "none" {
		return s, fmt.Errorf("SMTP_TLS deve ser starttls, tls ou none")
	}
	if (s.user == "") != (s.password == "") {
		return s, fmt.Errorf("SMTP_USER e SMTP_PASSWORD precisam ser configurados juntos")
	}
	if s.mode == "none" && s.user != "" {
		return s, fmt.Errorf("autenticação SMTP exige TLS")
	}
	return s, nil
}

func (s smtpSender) Send(p Pedido) error {
	addr := net.JoinHostPort(s.host, s.port)
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tlsConfig := &tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	var err error
	if s.mode == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	c, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return err
	}
	defer c.Close()
	if s.mode == "starttls" {
		if err = c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if s.user != "" {
		if err = c.Auth(smtp.PlainAuth("", s.user, s.password, s.host)); err != nil {
			return err
		}
	}
	if err = c.Mail(s.from); err != nil {
		return err
	}
	if err = c.Rcpt(p.Email); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Pedido #%d recebido\r\nMessage-ID: <pedido-%d@ms-notificacoes>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\nOlá, %s!\r\nRecebemos seu pedido #%d.\r\nEsta mensagem confirma o recebimento, não a disponibilidade de estoque ou a conclusão do preparo.\r\n", s.from, p.Email, p.PedidoID, p.PedidoID, p.Nome, p.PedidoID)
	if _, err = w.Write([]byte(msg)); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	// DATA aceito já significa envio aceito; falha no QUIT não deve reenviar.
	_ = c.Quit()
	return nil
}
