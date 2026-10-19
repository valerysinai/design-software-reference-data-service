# ADR R1: outbox transaccional, inbox idempotente y recuperación manual

Estado: implementado; el informe R1 registra las ejecuciones verificadas.

## Contexto

El reto R1 del plan individual exige outbox, consumidor idempotente con tabla de deduplicación, backoff exponencial, DLQ reprocesable y caos automatizado con caída del broker y reinicio del servicio. La base HU-REF-019 ya guarda cambios de catálogos/centros y sus eventos en una transacción, pero no incluía un consumidor con efecto transaccional deduplicado ni demostraba toda la recuperación.

## Decisión

Mantener el outbox existente y reutilizar sus eventos de catálogo y centro. El relay publica el `event_id`, payload y fecha comprometidos en PostgreSQL, sin generar una identidad ni una fecha distintas en cada reintento. El estado `published` indica entrega confirmada al broker, incluida la entrega a DLQ cuando corresponde; no equivale por sí solo a procesamiento exitoso del consumidor.

Añadir una proyección asíncrona operativa del último evento por agregado (`event_projection`). No sustituye las tablas de negocio: conserva el último payload de evento, su tipo/fecha y el número de eventos distintos aplicados. La capa de aplicación valida el sobre y llama a un puerto de persistencia; el adaptador PostgreSQL controla la transacción.

En una misma transacción se inserta `(consumer_name,event_id)` en `event_inbox` y se actualiza la proyección. La clave primaria evita carreras entre consumidores. Si el efecto falla, se revierte también el inbox. Si el commit fue exitoso pero el ACK se pierde, una entrega repetida no vuelve a aplicar el efecto. Un hash del tipo/payload detecta que alguien reutilice un ID con contenido distinto.

El consumidor usa ACK manual después del commit y QoS 1. En fallos de procesamiento publica una copia persistente a colas de espera de 1, 2 y 4 segundos. Exige confirmación del broker antes de reconocer el mensaje original; tras el intento inicial y tres reintentos, lo lleva a `reference-data.projection.dlq`. Si el envío de recuperación falla, el original se rechaza con requeue y se reconecta.

Las colas nuevas son quorum. Las colas de retry usan dead lettering `at-least-once`, overflow `reject-publish` y retorno a la cola principal. La DLQ R1 no tiene TTL, para que los mensajes esperen una intervención sin expirar. El outbox que no puede publicarse permanece en PostgreSQL. No se afirma entrega exactamente una vez: se combina entrega al menos una vez con efecto idempotente en PostgreSQL.

El operador corrige la causa y ejecuta `cmd/reference-data-replay` con un límite explícito. El comando conserva cuerpo e ID, reinicia el contador de retry y espera confirmación antes de quitar el mensaje de la DLQ. La nueva aplicación continúa pasando por la deduplicación.

## Alternativas

- Memoria o mapa local para deduplicar: pierde el estado en reinicios y no coordina instancias; descartado.
- Guardar inbox y efecto en transacciones separadas: permite perder el efecto o repetirlo después de una caída; descartado.
- Reconocer antes del commit o de confirmar el reenvío: crea una ventana de pérdida; descartado.
- Reintentar sin límite con `nack/requeue`: produce un bucle caliente que bloquea la cola; descartado para errores de procesamiento.
- Presentar una simulación o insertar manualmente filas de outbox como prueba de caos: no demuestra el flujo de negocio; la prueba usa HTTP real con JWT de IAM y detiene el contenedor RabbitMQ.

## Verificación

El runner aislado crea tres catálogos por HTTP con broker disponible, detiene RabbitMQ, crea tres más y comprueba sus seis filas de outbox. Reinicia Reference Data antes de recuperar el broker y exige seis eventos publicados, seis claves inbox y seis efectos. Publica doce duplicados deliberados y espera un evento posterior como barrera FIFO antes de comprobar que no hubo efectos duplicados.

Otra prueba dispara un error real de PostgreSQL después de insertar la clave inbox; comprueba rollback, los tres reintentos, la DLQ y el reprocesamiento exitoso tras retirar la falla. También compite con doce entregas concurrentes de un mismo evento.

## Consecuencias y límites

El ambiente local usa un solo nodo RabbitMQ con datos persistentes: prueba indisponibilidad y reinicio, no pérdida definitiva de discos ni alta disponibilidad multinodo. Las garantías dependen de conservar PostgreSQL y los volúmenes del broker. El inbox debe retenerse tanto como puedan reprocesarse los eventos; no se agrega una limpieza automática que invalide esa condición.

Se mantienen las rutas y contratos HTTP y la validación real de IAM. La DLQ histórica de HU-REF-019 se conserva para compatibilidad; el relay R1 y el nuevo consumidor usan la DLQ de proyección sin expiración. Los nuevos esquemas y permisos pertenecen a Reference Data DB; la orquestación y los secretos locales pertenecen a Docker Infra.

Referencia técnica: [RabbitMQ 3.13: quorum queues y dead lettering al menos una vez](https://www.rabbitmq.com/docs/3.13/quorum-queues#dead-lettering).
