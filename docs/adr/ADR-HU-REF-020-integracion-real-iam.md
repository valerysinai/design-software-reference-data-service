# ADR HU-REF-020: validar seguridad con IAM y PostgreSQL reales

Estado: decisión implementada; resultados de ejecución documentados por separado.

## Contexto

HU-REF-020 requiere un entorno reproducible entre Reference Data e IAM: Java y reactor correctos, esquema y usuarios reales, JWT válidos y permisos por endpoint. La evidencia histórica se agrupó con HU-REF-019 y no garantiza por sí sola el estado de una copia nueva. El script anterior suponía contenedores y puertos de una sesión personal.

## Decisión

Reutilizar la prueba de parámetros con IAM real y exponerla con el nombre `TestHURef020ParametersWithRealIAMJWT`. Se preserva la entrada HU-REF-019 y sus variables para compatibilidad. El runner de HU-REF-020 usa variables propias y exige configuración completa, evitando falsos éxitos por pruebas omitidas.

La orquestación pertenece a `docker-infra/validation/hu-ref-020`. Compila el reactor IAM con Java 21, ejecuta los changelogs de ambos dominios en una base aislada, prepara cuentas oficiales de prueba y arranca Reference Data desde su código. La preparación de fixtures IAM pertenece a `design-software-iam-db/test/hu-ref-020`, no a este microservicio. Cada aplicación usa su propio usuario PostgreSQL con rol de dominio; el propietario de migraciones se usa solo en tooling/pruebas.

El JWT se obtiene mediante login HTTP real. Reference Data verifica su firma contra JWKS real y comprueba features. La prueba exige permisos efectivos para ADMIN_STAFF, denegación a LEARNER y rechazo de firmas alteradas; no basta con decodificar el payload del JWT.

Para la validación local en Docker, ambos procesos comparten el espacio de red con puertos distintos. JWKS se consulta por loopback y se conserva la regla existente de HTTPS fuera de localhost. No se añade ninguna excepción de seguridad para nombres arbitrarios de contenedores.

## Alternativas consideradas

- JWT firmado por la propia prueba o middleware simulado: no demuestra integración con IAM; descartado para esta HU.
- Reutilizar la base y los contenedores de una carpeta externa: no demuestra reproducibilidad de los repositorios entregados; descartado.
- Permitir JWKS HTTP para cualquier host Docker: ampliaría la excepción de seguridad; descartado.
- Separar redes y provisionar TLS local: válido para otro escenario, pero añade gestión de certificados que no es necesaria para probar el caso local.

## Consecuencias y límites

La primera ejecución requiere Docker, descargas y compilación. Los secretos se generan localmente y se excluyen de Git. Las fixtures conservan sus identificadores históricos para no duplicar seeds ni cambiar cuentas reales.

Esta decisión valida la HU de backend E1-E4. No acredita integración de microfrontend/web-shell, mensajería, idempotencia, DLQ ni caos del reto R1. Tampoco cierra una HU del repositorio original ni sustituye la revisión del instructor. Las pruebas reales y su informe deben acompañar la entrega.
