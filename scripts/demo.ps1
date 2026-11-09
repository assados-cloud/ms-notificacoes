param(
    [long]$PedidoId = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds(),
    [string]$ApiUrl = 'http://localhost:8082',
    [string]$RabbitUrl = 'http://localhost:15672',
    [string]$MailUrl = 'http://localhost:8025'
)
$ErrorActionPreference = 'Stop'
# Exclusivo do Compose local. Na integração, use POST /pedidos de verdade.
$basic = [Convert]::ToBase64String([Text.Encoding]::ASCII.GetBytes('app:local-only'))
$headers = @{ Authorization = "Basic $basic" }
$event = @{ pedido_id = $PedidoId; cliente_nome = 'Maria'; cliente_email = 'maria@example.com'; itens = @(@{ produto_id = 1; quantidade = 2 }); total = 44; criado_em = [DateTime]::UtcNow.ToString('o') } | ConvertTo-Json -Compress
$payload = @{ properties = @{ delivery_mode = 2; content_type = 'application/json' }; routing_key = ''; payload = $event; payload_encoding = 'string' } | ConvertTo-Json -Depth 5
$result = Invoke-RestMethod "$RabbitUrl/api/exchanges/%2F/pedido.criado/publish" -Method Post -Headers $headers -ContentType 'application/json' -Body $payload
if (-not $result.routed) { throw 'Sem fila destinatária. Aguarde consumidor pronto antes de publicar.' }
for ($i = 0; $i -lt 20; $i++) {
    Start-Sleep -Milliseconds 500
    try { $notification = Invoke-RestMethod "$ApiUrl/notificacoes/$PedidoId" } catch { continue }
    if ($notification.status -eq 'ENVIADO') {
        $notification | ConvertTo-Json
        Write-Host "Veja o e-mail em $MailUrl. Repita com o mesmo -PedidoId para verificar idempotência."
        exit 0
    }
}
throw "Notificação não enviada no prazo. Consulte /notificacoes/$PedidoId e os logs."
