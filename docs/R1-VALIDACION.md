# R1: guía de validación y sustentación

## Ejecución

Con Docker Desktop iniciado y los repositorios organizados como en HU-REF-020:

```powershell
./scripts/run-r1-verification.ps1
```

Reference Data Service, Reference Data DB y Docker Infra deben estar en `r1-resiliencia-mensajeria` con los cambios R1. IAM Service e IAM DB usan sus versiones integradas en `develop`.

El runner realiza la preparación y todas las fases; no es necesario detener contenedores manualmente. Usa Java 21, Go 1.26.5, PostgreSQL y RabbitMQ reales dentro de Docker. Genera credenciales locales y exige los PASS de cada prueba, sin aprobar pruebas omitidas.

| Requisito del PDF | Implementación | Verificación |
|---|---|---|
| Outbox transaccional | Se reutiliza `parameterization.outbox_event` dentro de la transacción de negocio | Catálogo y evento se guardan juntos; fallo de serialización revierte ambos |
| Consumidor idempotente | `event_inbox` y efecto en `event_projection` se guardan en una misma transacción | Doce entregas concurrentes producen un efecto; el fallo de la proyección no deja una clave inbox |
| Retry exponencial y DLQ | Colas quorum a 1, 2 y 4 segundos; confirmaciones antes de ACK; DLQ sin TTL | Un error real de PostgreSQL lleva el evento a DLQ después de tres reintentos |
| Reprocesamiento manual | `cmd/reference-data-replay -limit N` preserva cuerpo e ID | Tras retirar el error de PostgreSQL, replay produce un efecto |
| Caos automatizado | El runner apaga RabbitMQ a mitad del flujo y reinicia Reference Data | Seis eventos recuperados, doce duplicados deliberados y cero efectos duplicados |

## Qué se conserva del trabajo anterior

El outbox, sus escrituras transaccionales, el publicador AMQP y los eventos de catálogo/centro provienen de la base del proyecto. Esta entrega completa el consumidor/inbox, las garantías de reintento, la recuperación manual y su demostración reproducible. No se presenta como nueva toda la solución previa.

## Qué hace el consumidor

Mantiene una proyección operativa del último evento recibido por catálogo o centro: ID de agregado, tipo/payload del último evento, fecha y número de eventos distintos aplicados. No sustituye la tabla de negocio ni promete orden global entre agregados.

La clave `(consumer_name,event_id)` evita duplicados incluso entre transacciones concurrentes. El contador de la proyección permite demostrar que una entrega repetida no volvió a aplicar el efecto. Un hash detecta el uso del mismo ID con contenido distinto.

## Guion breve para explicar el código

1. Mostrar la transacción que escribe catálogo y outbox en `internal/adapter/out/postgres/hierarchy.go` y la prueba de rollback.
2. Mostrar `PublishPersisted`: publica el ID/payload/fecha comprometidos y espera publisher confirms; la caída conserva los pendientes en PostgreSQL.
3. Mostrar `internal/adapter/out/persistence/inbox.go`: inbox y proyección dentro de una transacción; duplicado no incrementa el efecto.
4. Mostrar `internal/adapter/out/messaging/consumer.go`: ACK después del commit; espera 1/2/4 segundos y DLQ; forward confirmado antes de ACK del original.
5. Ejecutar el runner y señalar las fases broker-stop, HTTP durante la caída, service-restart y recovery.
6. Mostrar el resultado de los seis eventos y la recuperación manual desde DLQ.

## Operación manual

El procedimiento de replay y los comandos para detener conservando datos están en `validation/r1-reference/README.md` de Docker Infra. Corregir la causa antes de reprocesar. No borrar inbox, no purgar DLQ y no eliminar los volúmenes como procedimiento de recuperación.

El ADR es [ADR-R1-resilience-and-messaging.md](adr/ADR-R1-resilience-and-messaging.md). El informe de la ejecución final se registra en `R1-RESULTADO.md`. Los logs originales se generan en `validation/r1-reference/evidence/` de infraestructura y se excluyen de Git.

## Alcance de la entrega

Los PR deben separarse por los tres repositorios modificados y apuntar a `develop`. Este reto individual no sustituye la integración de microfrontend/web-shell ni la sustentación personal del plan. La prueba cubre caída y reinicio conservando volúmenes; no afirma tolerancia a la pérdida permanente de discos.
