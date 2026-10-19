# Informe técnico R1: resiliencia y mensajería

## Resultado

**PASS de los cuatro criterios técnicos R1 en entorno local real.** La ejecución final terminó el **19 de octubre de 2026 a las 12:49:48, hora de Colombia** (`2026-10-19T17:49:48Z`), con identificador `ffcb33176006`.

El recorrido completo de caída/recuperación se ejecutó dos veces con éxito. La versión final añadió y comprobó explícitamente la atomicidad del outbox, la detección de un mismo ID con contenido distinto y todos los marcadores PASS obligatorios. La aprobación académica y la sustentación personal siguen correspondiendo al instructor y a la aprendiz.

## Entorno

Proyecto Compose `r1-reference`, independiente del usado para HU-REF-020. PostgreSQL 16, RabbitMQ 3.13 con datos persistentes y colas quorum, IAM compilado con Java 21.0.12 y Maven 3.9.16, Reference Data y pruebas con Go 1.26.5. Las cuentas de aplicación usan roles de dominio; las claves, contraseñas y JWT no se publican.

Se aplicaron 37 changesets IAM y 20 Reference Data. De los últimos, 18 eran de la base y **dos son nuevos de R1**: tablas inbox/proyección y sus permisos. La repetición confirmó esquemas al día, sin changesets filtrados. Los cambios anteriores no se reescribieron.

## Evidencias por criterio

### A. Outbox transaccional

`TestR1TransactionalOutbox` verificó en PostgreSQL que la fila de catálogo y su evento se comprometen juntos. Un error de serialización después de insertar el catálogo revirtió tanto el dato como el evento.

```text
OUTBOX PASS: business row and event commit together; serialization failure rolls both back
--- PASS: TestR1TransactionalOutbox (0.07s)
```

Se aprovecha el outbox del proyecto base. La mejora R1 conserva ID, fecha y payload al publicar o reintentar. Si no puede publicar ni enviar a DLQ, el evento no se da por entregado y permanece en PostgreSQL.

### B. Consumidor idempotente

Doce invocaciones concurrentes contra PostgreSQL real compitieron por el mismo ID y produjeron **un efecto**. La prueba también rechazó reutilizar ese ID con un payload distinto, sin modificar la proyección.

```text
INBOX PASS: 12 concurrent deliveries, 1 committed effect
```

El efecto es la actualización de una proyección operativa del último evento por agregado. `event_inbox` y `event_projection` comparten una transacción. Un trigger de prueba hizo fallar el INSERT de la proyección **después** de insertar el inbox; se comprobó que no quedó una clave inbox que impidiera procesar el evento después de la recuperación.

### C. Retry exponencial, DLQ y recuperación manual

El error real de PostgreSQL provocó un intento inicial y **tres reintentos con esperas de 1, 2 y 4 segundos**. La DLQ recibió el mensaje con contador de intento 4, el mismo ID y exactamente el mismo cuerpo. Se comprobó que no se había aplicado el efecto ni persistido el inbox.

Después de retirar el trigger, se llamó al mecanismo de replay manual: sacó un mensaje de DLQ y lo aplicó una sola vez. La prueba usa RabbitMQ real, no un publicador simulado.

```text
RETRY/DLQ PASS: 1 initial attempt + retries at 1s/2s/4s; inbox rollback; manual replay preserved event_id and applied once
--- PASS: TestR1AtomicInboxAndDLQ (7.51s)
```

Se verificó además el comando operativo `go run -mod=readonly ./cmd/reference-data-replay -limit 1`. Al terminar todas las pruebas respondió `Replayed 0 messages; event IDs preserved.`, confirmando que no quedaban mensajes fallidos listos para replay.

Las colas retry son quorum con `x-dead-letter-strategy=at-least-once` y `x-overflow=reject-publish`. Sus TTL son 1000/2000/4000 ms. La DLQ R1 es durable y no tiene TTL. Los reenvíos y el replay esperan publisher confirms antes de reconocer el original.

### D. Caos real con reinicio del servicio

El runner realizó estas acciones verificables:

1. Creó tres catálogos por HTTP, con login/JWT real de IAM; esperó sus tres efectos.
2. **Detuvo el contenedor RabbitMQ**.
3. Comprobó que RabbitMQ era inaccesible y creó otros tres catálogos por HTTP; los seis cambios tenían su evento comprometido en outbox.
4. **Reinició Reference Data mientras RabbitMQ seguía detenido**.
5. Recuperó RabbitMQ y esperó la entrega y el procesamiento de los seis eventos.
6. Reenvió cada evento dos veces, **doce duplicados deliberados**, preservando los IDs.
7. Esperó un evento posterior como barrera FIFO del consumidor antes de medir los efectos finales.

| Medición del flujo de caos | Resultado |
|---|---:|
| Catálogos creados por HTTP | 6 |
| Eventos comprometidos en outbox | 6 |
| Eventos entregados al broker | 6 |
| Claves inbox de esos eventos | 6 |
| Filas de proyección de esos agregados | 6 |
| Total de efectos aplicados a esos agregados | 6 |
| Duplicados enviados deliberadamente | 12 |
| Eventos perdidos observados | **0** |
| Efectos duplicados observados | **0** |

```text
--- PASS: TestR1ChaosBaseline (15.60s)
--- PASS: TestR1ChaosDuringOutage (2.15s)
CHAOS PASS: 6 committed events, 6 published, 6 inbox records, 6 effects; broker stopped mid-flow and service restarted; 12 deliberate duplicates; lost=0 duplicated_effects=0
--- PASS: TestR1ChaosRecovery (21.53s)
```

Las cifras corresponden a los seis eventos identificados por el run ID, no a todas las fixtures del entorno. El evento barrera es adicional y no se incluye en esos seis. Las duraciones son de pruebas funcionales, no métricas de carga o p95.

## Regresión y reproducción

Pasaron los paquetes `internal/application/hierarchy` y `internal/adapter/out/messaging` en la ejecución de regresión. Las integraciones opcionales omitidas en esa ejecución sin variables externas no se contabilizan como evidencia R1: la evidencia real son las cinco pruebas obligatorias anteriores, todas en PASS sin Skip.

```powershell
./scripts/run-r1-verification.ps1
```

La [guía de validación y sustentación](R1-VALIDACION.md) explica el código y los pasos. El [ADR R1](adr/ADR-R1-resilience-and-messaging.md) justifica las decisiones. Los logs originales están en `validation/r1-reference/evidence/` de Docker Infra: `outbox.log`, `inbox-dlq.log`, `chaos-baseline.log`, `broker-stop.log`, `chaos-outage.log`, `service-restart.log`, `broker-recovery.log`, `chaos-recovery.log` y `result.json`. Son generados y se excluyen de Git; este informe conserva los extractos revisados.

## Responsabilidades y límites

Reference Data Service contiene el consumidor, la persistencia del inbox/proyección, el replay y las pruebas. Reference Data DB contiene solo changesets nuevos y sus rollbacks. Docker Infra contiene la orquestación y la prueba de caos. IAM no se modifica para este reto.

Se demuestra recuperación de indisponibilidad conservando los volúmenes; no se afirma tolerancia a su pérdida permanente ni alta disponibilidad multinodo. Tampoco se afirma entrega exactamente una vez: los duplicados pueden llegar, pero no repiten el efecto comprometido en PostgreSQL.
