# Informe técnico: HU-REF-020

## Resultado

**Validación local de E1-E4: PASS.** Ejecución final completada el 19 de octubre de 2026 a las 11:38:53, hora de Colombia (`2026-10-19T16:38:53Z`). El flujo se ejecutó dos veces con éxito; la ejecución final recreó los servicios y compiló IAM desde una copia nueva de las fuentes.

Esta evidencia corresponde a la HU de integración backend. No declara cerrada la HU en el tablero original ni completados el reto R1 o la integración de microfrontend/web-shell del plan de mejoramiento.

## Entorno comprobado

- Proyecto Docker Compose: `hu-ref-020`, base de datos aislada del mismo nombre.
- PostgreSQL: imagen `postgres:16-alpine`; migraciones oficiales de IAM y Reference Data.
- IAM: Java Temurin **21.0.12**, Maven **3.9.16**, reactor completo de cinco módulos.
- Reference Data y test E2E: imagen Go **1.26.5**, código de estos repositorios locales.
- IAM y Reference Data usan usuarios PostgreSQL distintos, miembros de `iam_writer` y `reference_writer`. El propietario de migraciones solo se usa para tooling y limpieza de pruebas.
- Clave RSA local generada dentro de Docker y credenciales aleatorias excluidas de Git. No se usan JWT prefabricados ni mocks.

## E1: arranque, reactor y migraciones

Las migraciones iniciales aplicaron **37 changesets de IAM y 18 de Reference Data**. La repetición final confirmó ambos changelogs al día: 37 y 18 previamente aplicados, cero filtrados y cero cambios pendientes.

IAM se construyó desde la raíz con `mvn -B -ntp -U -DskipTests install`. Resumen de la ejecución final:

```text
Reactor Summary for iam-service 0.1.0:
iam-service   SUCCESS
iam-core      SUCCESS
iam-contracts SUCCESS
iam-adapters  SUCCESS
iam-api       SUCCESS
BUILD SUCCESS
```

Hibernate validó el esquema al arrancar. Se obtuvieron respuestas HTTP 200 en IAM `/ready`, su endpoint JWKS y Reference Data `/health/ready`. La compilación del reactor omite la ejecución de la suite IAM; no se presenta esa compilación como prueba de toda la aplicación IAM.

## E2: datos de seguridad

Salida real del runner de fixtures:

```text
column_name
must_change_password
password_temporary_expires_at

iam_outbox
identity.outbox

email                              role
hu-ref-018-admin@example.test       ADMIN_STAFF
hu-ref-018-learner@example.test     LEARNER
```

Se reutilizaron las fixtures dev/qa oficiales. Sus nombres históricos no cambian la identidad HU-REF-020 de esta validación. Sus contraseñas no se incorporan al informe.

## E3 y E4: JWT y permisos reales

La prueba inició sesión mediante IAM, comprobó `iss`, `sub`, `exp`, roles y features con alcance `GLOBAL`, y consumió operaciones reales de Reference Data. El servicio obtuvo la clave pública de JWKS y validó la firma; una firma alterada fue rechazada.

| Escenario ejecutado | Esperado | Observado |
|---|---:|---:|
| ADMIN_STAFF lista parámetros | 200 | 200 |
| ADMIN_STAFF crea parámetro | 201 | 201 |
| ADMIN_STAFF modifica parámetro | 200 | 200 |
| LEARNER lista parámetros sin feature | 403 | 403 |
| LEARNER crea parámetro sin feature | 403 | 403 |
| LEARNER modifica parámetro sin feature | 403 | 403 |
| Consulta sin token | 401 | 401 |
| Consulta con token mal formado | 401 | 401 |
| Consulta con firma del JWT alterada | 401 | 401 |

Salida final:

```text
=== RUN   TestHURef020ParametersWithRealIAMJWT
E3 PASS: real IAM login; expected issuer, subject, expiry, roles and scoped features
E4 PASS: admin GET 200 / POST 201 / PUT 200; learner GET/POST/PUT 403; missing, malformed and modified-signature JWT 401
--- PASS: TestHURef020ParametersWithRealIAMJWT (1.07s)
PASS
ok github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/hierarchy 1.086s
```

El runner verificó el marcador PASS y registró `status: passed` con E1-E4 en `evidence/result.json`. No hubo omisión de la prueba E2E. El parámetro temporal creado se eliminó al finalizar.

Como comprobación adicional pasó el paquete de seguridad HTTP:

```text
go test -mod=readonly -count=1 ./internal/adapter/in/http/security
ok github.com/code-sena/design-software-reference-data-service/internal/adapter/in/http/security 0.730s
```

La duración indicada corresponde a esta ejecución funcional, no a una prueba de carga ni a una métrica p95.

## Reproducción y archivos

Desde Reference Data Service, con los repositorios organizados como indica [la guía](HU-REF-020-VALIDACION.md):

```powershell
./scripts/run-hu-ref-020-e2e.ps1
```

El ADR [HU-REF-020](adr/ADR-HU-REF-020-integracion-real-iam.md) justifica la prueba real, las responsabilidades por repositorio y la configuración de JWKS local sin modificar la regla de HTTPS fuera de localhost.

Los resultados originales quedan en `validation/hu-ref-020/evidence/` del repositorio de infraestructura: `migrations-iam.log`, `migrations-reference.log`, `schema-and-fixtures.log`, `java-runtime.log`, `maven-runtime.log`, `e2e.log`, `services.log` y `result.json`. Son archivos generados excluidos de Git; este informe conserva los extractos revisados para la entrega.

## Límites y siguiente entrega

No se reimplementó ni se atribuyó como nueva la solución histórica del instructor o del equipo. La entrega agrega la reproducción aislada y la evidencia verificable de HU-REF-020. No se consultó el estado del PR original por falta de acceso.

La prueba usa HTTP únicamente para JWKS por loopback dentro del espacio de red compartido de los dos contenedores. Para un despliegue separado debe configurarse JWKS mediante HTTPS. No se desactivaron certificados ni validaciones JWT.

La indisponibilidad inicial de Maven Central se resolvió reintentando descargas; la ejecución final compiló en verde. El flujo no requiere cambios de JDK en Windows ni contenedores de otra carpeta.

Quedan para la siguiente etapa la publicación/revisión de los PR de los tres repositorios modificados y el reto R1 en `r1-resiliencia-mensajeria`, con sus pruebas específicas de idempotencia, reintentos, DLQ y caos.
