package main

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSMTPDelivery(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan string, 1)
	go func() {
		c, err := listener.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprint(c, "220 localhost SMTP\r\n")
		scanner := bufio.NewScanner(c)
		var body strings.Builder
		data := false
		for scanner.Scan() {
			line := scanner.Text()
			if data {
				if line == "." {
					data = false
					received <- body.String()
					fmt.Fprint(c, "250 accepted\r\n")
				} else {
					body.WriteString(line + "\n")
				}
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				fmt.Fprint(c, "250 localhost\r\n")
			case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
				fmt.Fprint(c, "250 OK\r\n")
			case line == "DATA":
				data = true
				fmt.Fprint(c, "354 send data\r\n")
			case line == "QUIT":
				fmt.Fprint(c, "221 bye\r\n")
				return
			default:
				fmt.Fprint(c, "500 invalid command\r\n")
			}
		}
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	s := smtpSender{host: host, port: port, from: "pedidos@example.com", mode: "none"}
	if err = s.Send(Pedido{PedidoID: 42, Nome: "Maria", Email: "maria@example.com"}); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-received:
		for _, want := range []string{"To: maria@example.com", "Subject: Pedido #42 recebido", "Message-ID: <pedido-42@ms-notificacoes>", "Olá, Maria!"} {
			if !strings.Contains(body, want) {
				t.Errorf("mensagem sem %q", want)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("SMTP não recebeu mensagem")
	}
}
