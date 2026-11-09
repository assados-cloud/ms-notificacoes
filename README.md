# Notificações da Lanchonete

Microsserviço de apoio em **Go**, com **PostgreSQL próprio**, RabbitMQ e envio SMTP. Recebe o evento `pedido.criado` publicado por `assados-cloud/ms-pedidos` e envia um e-mail de **pedido recebido**. Não afirma que o estoque foi aprovado nem que o preparo terminou.

## Responsabilidade e rubrica

- **R1:** serviço independente, responsabilidade identificável, Dockerfile e persistência exclusiva em `notificacoesdb`. Não acessa tabelas de Pedidos, Cardápio ou Estoque. Go acrescenta uma linguagem; a equipe já usa JavaScript/Python e MySQL/PostgreSQL.
- **R2:** manifests para Deployment, Service ClusterIP, probes, recursos, PVC e NGINX Ingress no EKS. Comunicação por nomes de Services (`rabbitmq`, `postgres-notificacoes`). O balanceamento entre réplicas deve ser demonstrado pela equipe, por exemplo no Cardápio; não basta este repositório conter manifests.
- **R3:** consumo e processamento funcional de mensagens, e API REST de consulta. A comunicação REST **entre serviços** continua sendo Pedidos → Cardápio; esta API isolada não substitui essa integração.

Fluxo: Pedidos → exchange fanout `pedido.criado` → fila própria `notificacoes` → PostgreSQL → SMTP. Estoque usa outra fila e recebe sua própria cópia do evento.

## Contrato com Pedidos

Exchange durável `pedido.criado`, tipo **fanout**. Mesmo broker/vhost de Pedidos e Estoque. O consumidor declara a fila durável `notificacoes` e seu binding. **Suba o consumidor e aguarde `consumidor pronto` antes de criar o primeiro pedido**: eventos anteriores à criação da fila não são recuperados.

```json
{
  "pedido_id": 1,
  "cliente_nome": "Maria",
  "cliente_email": "maria@example.com",
  "itens": [{"produto_id": 1, "quantidade": 2}],
  "total": 44,
  "criado_em": "2026-11-10T23:30:00Z"
}
```

`pedido_id` deve ser inteiro positivo. Nome e e-mail são obrigatórios; os demais campos são ignorados pelo consumidor. Não use a fila `estoque`. Eventos inválidos são rejeitados sem requeue e encaminhados pelo exchange `notificacoes.dlx` à fila `notificacoes.invalidas`, para inspeção manual.

### Persistência e falhas

O evento é inserido em `notificacoes`, com `pedido_id` como chave primária. O ACK acontece **depois** da gravação durável, não depois do SMTP. A própria tabela funciona como fila de trabalho persistente: perder o processo depois do ACK não perde o trabalho pendente.

O worker usa `FOR UPDATE SKIP LOCKED` para impedir que réplicas enviem simultaneamente o mesmo registro. Falhas SMTP recebem até **5 tentativas**, com intervalos de 5, 10, 20 e 40 segundos. Estados: `PENDENTE`, `ENVIADO`, `FALHOU`. Após a quinta falha o histórico permanece consultável; não há reenvio administrativo automático. Eventos repetidos não reiniciam notificações existentes.

**Limitação:** SMTP e PostgreSQL não compartilham uma transação. Queda após aceitação pelo SMTP, antes do commit, pode gerar envio duplicado. O Message-ID é estável, mas não garante deduplicação pelo provedor. `ENVIADO` significa aceito pelo servidor SMTP, não prova de entrega à caixa final. Falhas durante uma transação podem não contabilizar uma tentativa concluída.

## Executar localmente

Requisitos: Docker Desktop com engine Linux e Docker Compose. Não precisa instalar Go para executar a aplicação.

```powershell
docker compose up --build -d
docker compose logs -f ms-notificacoes
```

Após `consumidor pronto`, em outro terminal:

```powershell
Invoke-RestMethod http://localhost:8082/notificacoes/health
./scripts/demo.ps1 -PedidoId 101
./scripts/demo.ps1 -PedidoId 101
```

Abra **http://localhost:8025** para ver o e-mail capturado pelo Mailpit. A segunda publicação não deve gerar outro e-mail. Mailpit captura SMTP local e **não entrega na Internet**. RabbitMQ Management: http://localhost:15672, usuário `app`, senha `local-only`. Essas credenciais são exclusivas do Compose local.

```powershell
Invoke-RestMethod http://localhost:8082/notificacoes/101
docker compose stop mailpit
./scripts/demo.ps1 -PedidoId 102
# O script falha por timeout; a aplicação continua tentando e depois registra FALHOU.
docker compose start mailpit
```

Para demonstrar retomada, reinicie Mailpit antes do limite de tentativas. Para encerrar mantendo os dados: `docker compose down`.

## API REST

| Método | Rota | Resultado |
|---|---|---|
| GET | `/notificacoes/{pedido_id}` | Estado, tentativas e datas; 404 se ainda não recebido |
| GET | `/notificacoes/health` | Readiness: testa PostgreSQL; 503 se indisponível |
| GET | `/notificacoes/live` | Liveness do processo |

Exemplo:

```json
{"pedido_id":101,"status":"ENVIADO","tentativas":1,"criado_em":"2026-11-10T23:30:00Z","enviado_em":"2026-11-10T23:30:01Z"}
```

A API não expõe nome/e-mail do cliente. Não há autenticação neste escopo acadêmico. Readiness avalia a API/banco, não a conectividade SMTP/RabbitMQ: confira os logs do consumidor e o resultado de uma notificação para validar a integração.

## Configuração

| Variável | Uso |
|---|---|
| `DATABASE_URL` | Obrigatória; PostgreSQL exclusivo do serviço |
| `RABBITMQ_URL` | Obrigatória; URL AMQP, mesmo vhost de Pedidos |
| `SMTP_HOST` | Obrigatória; hostname SMTP |
| `SMTP_FROM` | Obrigatória; endereço simples, autorizado pelo provedor |
| `SMTP_PORT` | Padrão `587` |
| `SMTP_TLS` | `starttls` (padrão), `tls` (TLS implícito, geralmente 465), `none` (somente teste local) |
| `SMTP_USER`, `SMTP_PASSWORD` | Opcionais em conjunto; autenticação exige TLS |
| `PORT` | Porta HTTP, padrão `8080` |

Nunca versione `.env.eks` ou credenciais reais. URLs de conexão precisam de percent-encoding quando senhas contêm caracteres especiais.

## Implantar no EKS da equipe

O repositório não cria cluster, RabbitMQ ou controlador NGINX. Use os componentes compartilhados da equipe. O PostgreSQL requer StorageClass padrão e provisionamento de PVC funcionando (por exemplo EBS CSI no ambiente da equipe).

Os comandos abaixo são **PowerShell**. Confira contexto e namespace antes de aplicar. Use arquitetura compatível com os nós e uma tag fixa, sem `latest`.

```powershell
kubectl config current-context
kubectl get storageclass
$ns = 'NAMESPACE_DA_EQUIPE'
$image = 'REGISTRY/ms-notificacoes:TAG'
docker buildx build --platform linux/amd64 --tag $image --push .
Copy-Item .env.eks.exemplo .env.eks
# Edite .env.eks com credenciais do ambiente e SMTP real.
kubectl -n $ns create secret generic ms-notificacoes --from-env-file=.env.eks --dry-run=client -o yaml | kubectl apply -f -
kubectl -n $ns apply -f k8s/postgres.yaml
kubectl -n $ns rollout status deployment/postgres-notificacoes --timeout=180s
kubectl -n $ns apply -f k8s/service.yaml
kubectl set image --local -f k8s/app.yaml ms-notificacoes=$image -o yaml | kubectl -n $ns apply -f -
kubectl -n $ns rollout status deployment/ms-notificacoes --timeout=180s
kubectl -n $ns apply -f k8s/ingress.yaml
```

`k8s/app.yaml` contém a imagem placeholder `ms-notificacoes:local`: substitua pelo comando acima. Se a equipe mantém um Ingress compartilhado, incorpore somente a regra `/notificacoes` nele e não aplique um segundo recurso com a mesma rota. Preserve o prefixo até a API; não use rewrite para remover `/notificacoes`. Se houver hostname específico, ajuste a regra ao hostname da equipe.

Para testar SMTP no cluster sem provedor, a equipe pode configurar um capturador SMTP interno; isso demonstra envio capturado, não entrega externa. Para entrega real, configure um servidor SMTP autorizado, com TLS. Não publique credenciais nos comandos de apresentação.

## Roteiro de apresentação

1. Mostrar Deployment/Pod/Service e banco exclusivo; explicar seu papel de apoio.
2. Verificar `consumidor pronto fila=notificacoes` nos logs.
3. Criar pedido pelo **POST /pedidos real** da equipe, com nome, e-mail e itens.
4. Consultar `GET http://HOST_DO_INGRESS/notificacoes/ID` até `ENVIADO`.
5. Mostrar e-mail recebido/capturado e log com o mesmo `pedido_id`.
6. Mostrar a baixa do Estoque; ambos processaram cópias do mesmo evento.
7. O responsável por Cardápio demonstra REST por Service DNS e balanceamento entre réplicas.

Não confundir o script de publicação isolada com a demonstração completa: a avaliação exige o fluxo integrado no EKS.

## Testes

### Ambiente local Kubernetes

Cluster de demonstração: `kind-notificacoes-local`; namespace `notificacoes-local`. É independente do Docker Compose e **não substitui o EKS**. O arquivo `k8s/local-dependencies.yaml` contém RabbitMQ/Mailpit somente para testes, com credenciais locais e dados efêmeros. Não o aplique no cluster compartilhado da equipe.

```powershell
docker compose ps
kubectl --context kind-notificacoes-local -n notificacoes-local get pods -o wide
kubectl --context kind-notificacoes-local -n notificacoes-local get deploy,svc,pvc
kubectl --context kind-notificacoes-local -n notificacoes-local logs deployment/ms-notificacoes --tail=20
```

Para acompanhar mudanças ao vivo, acrescente `--watch` ao comando `get pods`. Para acessar o Kubernetes pelo navegador, mantenha cada port-forward em um terminal separado:

```powershell
kubectl --context kind-notificacoes-local -n notificacoes-local port-forward service/ms-notificacoes 8083:80
kubectl --context kind-notificacoes-local -n notificacoes-local port-forward service/mailpit 8026:8025
kubectl --context kind-notificacoes-local -n notificacoes-local port-forward service/rabbitmq 15673:15672
```

Compose: API `8082`, e-mails `8025`, RabbitMQ `15672`. Kubernetes via port-forward: API `8083`, e-mails `8026`, RabbitMQ `15673`. São bancos e filas diferentes; criar um evento em um ambiente não o cria no outro. Port-forward é acesso local de teste, não demonstra o requisito NGINX Ingress do EKS.

Com Go 1.26 ou superior:

```powershell
go test ./...
go vet ./...
go build ./...
```

Os testes cobrem validação do contrato, proteção de configuração SMTP e política de tentativas. `TestDatabaseIntegration` também exercita chave única, falhas, limite de tentativas, sucesso e consulta HTTP quando `TEST_DATABASE_URL` aponta para um **banco descartável e exclusivo**. Sem essa variável, ele é explicitamente ignorado. O teste pressupõe ausência de outros workers e notificações pendentes nesse banco.
