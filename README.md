# design-software-reference-data-service

Servicio Go de datos referenciales para la plataforma SENA. Arquitectura
hexagonal (ADR-006): `cmd → adapter → application → domain`. Contract-first
(SDD) · TDD · OpenTelemetry.

---

## HU-REF-010 — Levantado local E2E

Esta sección documenta el procedimiento reproducible para ejecutar el
microservicio `reference-data-service` de extremo a extremo en un ambiente
local de desarrollo, usando la infraestructura compartida de `docker-infra`
(PostgreSQL, RabbitMQ, OTel) junto con el servicio IAM.

### Arquitectura del levantado local

```
┌─────────────────────────────────────────────────────────────────┐
│  docker-infra                                                    │
│                                                                  │
│  PostgreSQL :15432   RabbitMQ :5672 / :15672                    │
│  OTel Collector :4318   Grafana :3000   Tempo   Prometheus       │
└─────────────────────────────────────────────────────────────────┘
         ▲ JDBC/DSN           ▲ AMQP            ▲ OTLP/HTTP
         │                    │                  │
┌────────┴───────┐   ┌──────┴────────┐   ┌─────┴────────────────┐
│  IAM service   │   │  RabbitMQ     │   │  reference-data-svc  │
│  :8081         │   │  (eventos)    │   │  :8080               │
│  POST /login   │   └───────────────┘   │  GET/POST /macro…    │
│  JWKS endpoint │◄──────────────────────│  GET /reports/…      │
└────────────────┘                       └──────────────────────┘
```

Todos los servicios apuntan al **mismo PostgreSQL** gestionado por
`docker-infra` (base `design-software-develop`, usuario
`design_software_user`). Los esquemas de reference
(`institutional_structure`, `parameterization`) y los de IAM (`identity`,
`rbac`, `session`) coexisten en la misma base sin conflicto.

### Ambientes disponibles

`docker-infra` soporta 4 ambientes aislados por `COMPOSE_PROJECT_NAME`.
Elegir uno antes de empezar y usar su puerto consistently:

| Ambiente | `POSTGRES_PORT` | `COMPOSE_PROJECT_NAME` |
| -------- | --------------- | ---------------------- |
| develop  | `15432`         | `ds-develop`           |
| qa       | `15433`         | `ds-qa`                |
| staging  | `15434`         | `ds-staging`           |
| main     | `15435`         | `ds-main`              |

> En esta guía se usa **develop** (puerto `15432`). Para otro ambiente,
> reemplazar `15432` por el puerto correspondiente.

### Requisitos previos

| Herramienta       | Versión mínima | Uso                                   |
| ----------------- | -------------- | ------------------------------------- |
| Docker Desktop    | 4.x            | Contenedores PostgreSQL, RabbitMQ, OTel |
| Go                | 1.25           | Compilar y ejecutar reference-data-service |
| Java (JDK 21)    | 21             | Ejecutar IAM service (requisito estricto) |
| Maven             | 3.9+           | Compilar IAM service                    |
| curl              | cualquier      | Peticiones HTTP                         |
| openssl           | 3.x            | Generar clave RSA para IAM              |

### Repositorios necesarios

| Repositorio                              | Ruta sugerida                                |
| ---------------------------------------- | -------------------------------------------- |
| design-software-reference-data-service   | `C:\reference-data\design-software-reference-data-service`  |
| design-software-docker-infra             | `C:\reference-data\docker-infra\design-software-docker-infra`            |
| design-software-reference-data-db        | `C:\reference-data\database\design-software-reference-data-db`       |
| design-software-iam-service              | `C:\reference-data\design-software-iam-service`             |

### Orden de dependencias entre módulos

El orden recomendado de levantado en la plataforma es:

```
reference → iam → academic → training → actors → scheduling → document → monitoring → audit
```

Para esta E2E solo se necesitan `reference` e `iam`.

---

### Paso 1 — `.env` de docker-infra

Desde `C:\reference-data\docker-infra\design-software-docker-infra`, crear el archivo `.env` a partir de
`.env.example` si no existe:

```powershell
cd C:\reference-data\docker-infra\design-software-docker-infra
copy .env.example .env
```

Verificar que el contenido tenga al menos estas variables (ajustar si ya
existía):

```env
POSTGRES_DB=design-software-develop
POSTGRES_USER=design_software_user
POSTGRES_PASSWORD=change-me
POSTGRES_PORT=15432
COMPOSE_PROJECT_NAME=ds-develop
APP_DB_USER=design_software_app
APP_DB_PASSWORD=change-me-app
DB_REPOS_DIR=../../database
```

> **Importante:** `DB_REPOS_DIR` debe apuntar a la carpeta que contiene los
> repos `-db`. El valor `../../database` asume que `docker-infra` está en
> `C:\reference-data\docker-infra\design-software-docker-infra` y los repos
> `-db` están en `C:\reference-data\database\`. Ajustar si la estructura local
> es diferente.

---

### Paso 2 — Levantar infraestructura compartida (docker-infra)

Levantar PostgreSQL, RabbitMQ y el stack de observabilidad:

```powershell
cd C:\reference-data\docker-infra\design-software-docker-infra
docker compose up postgres -d
docker compose --profile broker --profile observability up -d
```

Verificar que los servicios estén saludables:

```powershell
docker compose ps
```

Puertos esperados:

| Servicio       | Puerto host | Usuario / Contraseña                    |
| -------------- | ----------- | --------------------------------------- |
| PostgreSQL     | 15432       | `design_software_user` / `change-me`    |
| RabbitMQ AMQP  | 5672        | `app` / `app`                           |
| RabbitMQ UI    | 15672       | `app` / `app`                           |
| OTLP HTTP      | 4318        | —                                       |
| OTLP gRPC      | 4317        | —                                       |
| Grafana        | 3000        | `admin` / `admin`                       |

---

### Paso 3 — Ejecutar migraciones reference-data (Liquibase)

El servicio `liquibase-reference` de `docker-infra` monta el repositorio
`design-software-reference-data-db` y ejecuta el changelog completo
(`changelog/changelog-master.yaml`), que incluye DDL, DML, DCL y TCL.

```powershell
cd C:\reference-data\docker-infra\design-software-docker-infra
docker compose --profile tooling run --rm liquibase-reference update
```

Resultado esperado: `Liquibase: Update has been successful.`

Ejecutar esta migración después de actualizar el código IAM. La versión actual
agrega las columnas de restablecimiento de contraseña y la tabla
`identity.outbox`, necesarias para que IAM pueda iniciar y registrar el login.

Esto crea:

**Esquemas y tablas:**

- `institutional_structure.macroregion`
- `institutional_structure.microregion`
- `institutional_structure.department`
- `institutional_structure.municipality`
- `institutional_structure.training_center`
- `institutional_structure.institutional_unit`
- `parameterization.catalog`
- `parameterization.catalog_detail`
- `parameterization.parameter`

**Roles de aplicación (DCL):**

- `reference_reader` — solo `SELECT` en ambos esquemas
- `reference_writer` — `SELECT, INSERT, UPDATE, DELETE` + secuencias
- `reference_admin` — `ALL PRIVILEGES`

> Estos roles son creados automáticamente por `03_dcl/00_roles/001_create_roles.sql`
> durante la migración. El servicio Go los usa vía `SET LOCAL ROLE` dentro de
> cada transacción.

**Datos semilla (DML):**

La migración inserta una cadena de demo con UUIDs fijos:

- Regional Antioquia (`ANT`) → Valle de Aburra (`ABU`) → Antioquia (DANE `05`)
  → Medellín (DANE `05001`) → Centro de Comercio y Servicios (code `9201`)

---

### Paso 4 — Ejecutar migraciones de IAM

IAM necesita sus propios esquemas (`identity`, `rbac`, `session`,
`identity_audit`, `rbac_catalog`) en la misma base de datos.

```powershell
cd C:\reference-data\docker-infra\design-software-docker-infra
docker compose --profile tooling run --rm liquibase-iam update
```

Resultado esperado: `Liquibase: Update has been successful.`

#### Cambios sincronizados en `design-software-iam-db`

Durante la prueba E2E se detectó que el código actual de `iam-service` estaba
más avanzado que el esquema persistente de PostgreSQL. IAM podía compilar, pero
no podía iniciar correctamente ni completar el login.

Archivos agregados o actualizados para corregir esa desalineación:

- `01_ddl/04_alter/006_add_password_reset_columns.sql`: agrega a
  `identity."user"` las columnas `must_change_password` y
  `password_temporary_expires_at`, utilizadas por la entidad JPA y los flujos
  de restablecimiento de contraseña.
- `01_ddl/04_alter/007_create_identity_outbox.sql`: crea
  `identity.outbox`, utilizada para registrar eventos transaccionales como el
  login y para el relay hacia RabbitMQ.
- `01_ddl/04_alter/changelog.yaml`: registra los changesets
  `password-reset-columns-iam-952` e `identity-outbox-iam-953`.
- `05_rollbacks/01_ddl/04_alter/006_add_password_reset_columns.rollback.sql`:
  rollback de las columnas de contraseña temporal.
- `05_rollbacks/01_ddl/04_alter/007_create_identity_outbox.rollback.sql`:
  rollback de la tabla outbox.

Los errores observados antes de aplicar estos changesets fueron:

```text
Schema-validation: missing column [must_change_password] in table [identity."user"]
BadSqlGrammarException: INSERT INTO identity.outbox ...
```

La causa no era un error del JWT ni del flujo de `reference-data`: la base IAM
estaba usando una versión anterior del changelog. QA debe ejecutar siempre:

```powershell
docker compose --profile tooling run --rm liquibase-iam update
```

antes de iniciar IAM. La verificación mínima es:

```powershell
docker compose exec postgres psql -U design_software_user -d design-software-develop -c "
  SELECT column_name FROM information_schema.columns
  WHERE table_schema = 'identity' AND table_name = 'user'
    AND column_name IN ('must_change_password', 'password_temporary_expires_at');
  SELECT to_regclass('identity.outbox');
"
```

El resultado debe mostrar las dos columnas y `identity.outbox`.

#### Verificar esquemas

```powershell
docker compose exec postgres psql -U design_software_user -d design-software-develop -c "
  SELECT schema_name FROM information_schema.schemata
  WHERE schema_name IN (
    'identity','rbac','session','identity_audit','rbac_catalog',
    'institutional_structure','parameterization'
  )
  ORDER BY schema_name;
"
```

Resultado esperado: los 7 esquemas listados.

---

### Paso 5 — Crear usuario de prueba con rol SYSTEM_ADMIN

Para obtener un JWT válido, se necesita un usuario en la base de datos con una
contraseña conocida y el rol `SYSTEM_ADMIN` asignado.

#### 5.1 Generar hash BCrypt de la contraseña

Usar Java (disponible porque IAM requiere JDK 21):

```powershell
cd C:\reference-data\design-software-iam-service
```

Crear un archivo temporal `BCryptHelper.java`:

```java
import org.springframework.security.crypto.bcrypt.BCryptPasswordEncoder;
public class BCryptHelper {
    public static void main(String[] args) {
        System.out.println(new BCryptPasswordEncoder().encode(args[0]));
    }
}
```

Compilar y ejecutar:

```powershell
javac -cp "$(mvn -pl iam-core dependency:build-classpath -Dmdep.outputFile=CON -q)" BCryptHelper.java
java -cp ".;$(mvn -pl iam-core dependency:build-classpath -Dmdep.outputFile=CON -q)" BCryptHelper "Test@2026!"
```

Copiar el hash generado (inicia con `$2a$10$` o `$2b$12$`).

#### 5.2 Insertar usuario y asignar rol

```powershell
cd C:\reference-data\docker-infra\design-software-docker-infra
docker compose exec postgres psql -U design_software_user -d design-software-develop
```

Dentro de psql, reemplazar `<BCRYPT_HASH>` con el hash del paso anterior:

```sql
-- Crear usuario admin de prueba
INSERT INTO identity."user"
  (id, email, password_hash, first_name, last_name, actor_type, is_active,
   must_change_password, password_temporary_expires_at)
VALUES (
    'a0000000-0000-0000-0000-000000000001',
    'admin@sena.edu.co',
    '<BCRYPT_HASH>',
    'Admin',
    'QA',
    'USER',
    TRUE,
    FALSE,
    NULL
);

-- Asignar rol SYSTEM_ADMIN (global, sin training_center)
INSERT INTO rbac.user_role (user_id, role_id, assigned_by)
SELECT
    'a0000000-0000-0000-0000-000000000001',
    r.id,
    'a0000000-0000-0000-0000-000000000001'
FROM rbac.role r
WHERE r.name = 'SYSTEM_ADMIN';
```

Verificar:

```sql
SELECT u.email, r.name AS role
FROM identity."user" u
JOIN rbac.user_role ur ON ur.user_id = u.id
JOIN rbac.role r ON r.id = ur.role_id
WHERE u.email = 'admin@sena.edu.co';
```

Resultado esperado: una fila con `admin@sena.edu.co` y `SYSTEM_ADMIN`.

Salir de psql: `\q`

---

### Paso 6 — Iniciar IAM service

#### 6.1 Generar clave RSA para JWT

```powershell
cd C:\reference-data\design-software-iam-service
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out iam-dev-key.pem
```

#### 6.2 Configurar variables de entorno

```powershell
$env:IAM_DB_DSN="jdbc:postgresql://localhost:15432/design-software-develop"
$env:IAM_DB_USERNAME="design_software_user"
$env:IAM_DB_PASSWORD="change-me"
$env:IAM_JWT_ISSUER="https://iam.it.sena.local"
$env:IAM_JWT_PRIVATE_KEY=$(Get-Content -Raw iam-dev-key.pem)
$env:IAM_JWT_KEY_ID="iam-rsa-1"
$env:SERVER_PORT="8081"
```

> **Importante:** `IAM_JWT_ISSUER` debe ser el mismo valor que se configure en
> reference-data-service (`IAM_JWT_ISSUER`). El valor `https://iam.it.sena.local`
> es el issuer de desarrollo usado en las pruebas de integración de IAM.

#### 6.3 Ejecutar IAM

```powershell
cd C:\reference-data\design-software-iam-service
mvnw.cmd -DskipTests install
mvnw.cmd spring-boot:run -pl iam-api
```

El build requiere JDK 21. El primer comando instala el reactor completo
(`iam-core`, `iam-contracts`, `iam-adapters` e `iam-api`); ejecutar solamente
`spring-boot:run -pl iam-api` desde una instalación limpia produce un error de
dependencia para `iam-adapters`.

Si no hay JDK 21 instalado, se puede construir y ejecutar IAM con la imagen
`maven:3.9-eclipse-temurin-21`, montando el repositorio en `/workspace` y
conectando el contenedor a la red `ds-develop_default`.

Verificar que IAM responde:

```powershell
curl.exe http://localhost:8081/actuator/health
```

Resultado esperado: `200` con estado `UP`.

---

### Paso 7 — Obtener JWT vía IAM login

```powershell
curl.exe -X POST http://localhost:8081/api/v1/auth/login `
  -H "Content-Type: application/json" `
  -d '{"email":"admin@sena.edu.co","password":"Test@2026!"}'
```

Respuesta esperada:

```json
{
  "access_token": "eyJhbGciOiJSUzI1NiIs...",
  "refresh_token": "...",
  "token_type": "Bearer",
  "expires_in": 900,
  "user": {
    "id": "a0000000-0000-0000-0000-000000000001",
    "email": "admin@sena.edu.co",
    "full_name": "Admin QA",
    "roles": ["SYSTEM_ADMIN"],
    "features": [
      "REF_HIERARCHY_MANAGE:GLOBAL",
      "REF_HIERARCHY_VIEW:GLOBAL",
      "REF_CATALOG_MANAGE:GLOBAL",
      "REF_CATALOG_VIEW:GLOBAL",
      "REF_PARAMETER_MANAGE:GLOBAL",
      "REF_PARAMETER_VIEW:GLOBAL"
    ]
  }
}
```

Guardar el `access_token` para los siguientes pasos:

```powershell
$TOKEN="<pegar el access_token aquí>"
```

> **Nota:** El token expira en 15 minutos. Si se recibe `401 IAM_INVALID_CREDENTIALS`,
> verificar que el hash BCrypt en la base de datos corresponde a la contraseña
> `Test@2026!`. Si el token expiró durante la prueba, repetir el login.

---

### Paso 8 — Iniciar reference-data-service

#### 8.1 Configurar variables de entorno

```powershell
cd C:\reference-data\design-software-reference-data-service

# Base de datos (mismo PostgreSQL de docker-infra)
$env:REFERENCE_DATA_DATABASE_URL="postgres://design_software_user:change-me@localhost:15432/design-software-develop?sslmode=disable"

# IAM / seguridad (obligatorio)
$env:IAM_JWT_ISSUER="https://iam.it.sena.local"
$env:IAM_JWKS_URL="http://localhost:8081/api/v1/auth/.well-known/jwks.json"


# Puerto del servicio
$env:PORT="8080"

# Observabilidad (opcional, requiere collector OTel del Paso 2)
$env:OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"
$env:OTEL_EXPORTER_OTLP_INSECURE="true"

# Mensajería (opcional, requiere RabbitMQ del Paso 2)
$env:REFERENCE_DATA_AMQP_URL="amqp://app:app@localhost:5672/"
```


#### 8.2 Ejecutar el servicio

`DEV_MODE` no es una configuración soportada: las rutas funcionales siempre requieren
`Authorization: Bearer <JWT>`. Para ejecutar el servicio localmente se deben configurar
`IAM_JWT_ISSUER` e `IAM_JWKS_URL`; únicamente los health checks son públicos.

```powershell
go run .\cmd\reference-data-api
```

Resultado esperado:

```
api listening on :8080
```

#### 8.3 Verificar health checks

```powershell
curl.exe http://localhost:8080/health/live
# Esperado: {"status":"ok"}

curl.exe http://localhost:8080/health/ready
# Esperado: {"status":"ready"}
```

---

### Paso 9 — Flujo E2E: cadena de jerarquía completa

La jerarquía institucional requiere 6 niveles en orden. Cada nivel depende del
ID del nivel padre. Todos los endpoints requieren el header `Authorization:
Bearer <TOKEN>`.

#### 9.1 Crear macroregión

```powershell
curl.exe -s -X POST http://localhost:8080/macroregions `
  -H "Content-Type: application/json" `
  -H "X-Request-Id: e2e-qa-001" `
  -d '{"name":"Nororiental","code":"NO"}'
```

Respuesta esperada: `201 Created`. Guardar el `id` de la respuesta:

```powershell
$MACROREGION_ID="<id de la respuesta>"
```

#### 9.2 Crear microregión

```powershell
curl.exe -s -X POST http://localhost:8080/microregions `
  -H "Content-Type: application/json" `
  -H "X-Request-Id: e2e-qa-002" `
  -d "{`"name`":`"Norte de Santander`",`"code`":`"NNS`",`"macroregion_id`":`"$MACROREGION_ID`"}"
```

```powershell
$MICROREGION_ID="<id de la respuesta>"
```

#### 9.3 Crear departamento

```powershell
curl.exe -s -X POST http://localhost:8080/departments `
  -H "Content-Type: application/json" `
  -H "X-Request-Id: e2e-qa-003" `
  -d "{`"name`":`"Norte de Santander`",`"dane_code`":`"54`",`"microregion_id`":`"$MICROREGION_ID`"}"
```

```powershell
$DEPARTMENT_ID="<id de la respuesta>"
```

#### 9.4 Crear municipio

```powershell
curl.exe -s -X POST http://localhost:8080/municipalities `
  -H "Content-Type: application/json" `
  -H "X-Request-Id: e2e-qa-004" `
  -d "{`"name`":`"Cúcuta`",`"dane_code`":`"54001`",`"department_id`":`"$DEPARTMENT_ID`"}"
```

```powershell
$MUNICIPALITY_ID="<id de la respuesta>"
```

#### 9.5 Crear centro de formación

```powershell
curl.exe -s -X POST http://localhost:8080/training-centers `
  -H "Content-Type: application/json" `
  -H "X-Request-Id: e2e-qa-005" `
  -d "{`"name`":`"Centro de Formación Agroindustrial`",`"center_code`":`"CFAI`",`"municipality_id`":`"$MUNICIPALITY_ID`",`"address`":`"Calle 5 # 3-47`"}"
```

```powershell
$TRAINING_CENTER_ID="<id de la respuesta>"
```

#### 9.6 Verificar la cadena creada

```powershell
curl.exe -s http://localhost:8080/macroregions | ConvertFrom-Json | ConvertTo-Json -Depth 5

curl.exe -s http://localhost:8080/training-centers | ConvertFrom-Json | ConvertTo-Json -Depth 5
```

Resultado esperado: listas con los recursos creados, paginación incluida.

---

### Paso 10 — Reporte de catálogo activo

#### 10.1 Crear un catálogo de prueba

```powershell
curl.exe -s -X POST http://localhost:8080/catalogs `
  -H "Content-Type: application/json" `
  -H "X-Request-Id: e2e-qa-006" `
  -d '{"name":"Estados","code":"STATES","description":"Estados de registros"}'
```

Guardar el `id` de la respuesta:

```powershell
$CATALOG_ID="<id de la respuesta>"
```

#### 10.2 Crear detalles del catálogo

```powershell
curl.exe -s -X POST "http://localhost:8080/catalogs/$CATALOG_ID/details" `
  -H "Content-Type: application/json" `
  -H "X-Request-Id: e2e-qa-007" `
  -d '{"label":"Activo","code":"ACTIVE","display_order":1}'
```

#### 10.3 Consultar el reporte

```powershell
curl.exe -s "http://localhost:8080/reports/active-catalog?page=1&page_size=20"
```

Respuesta esperada: `200` con `data` (incluyendo el catálogo creado con sus
detalles) y `pagination`.

---

### Resumen del flujo E2E ejecutado

El flujo E2E se ejecutó exitosamente con los siguientes pasos:

1. **Migraciones:** `liquibase-reference update` creó 14 change sets exitosamente
2. **Jerarquía institucional:**
   - Macroregión "Nororiental" (NO) → ID: 9ee9360c-a5a7-4c4c-be9b-aa7b5d2fcaa1
   - Microregión "Norte de Santander" (NNS) → ID: be85354d-770c-443e-9b9b-f9d876eb2208
   - Departamento "Norte de Santander" (DANE 54) → ID: 9a8cefc2-11e6-457f-ba99-5d3d1263b218
   - Municipio "Cúcuta" (DANE 54001) → ID: 8818ae18-279c-429a-8d6f-bc1306a69240
   - Centro de formación "Centro de Formación Agroindustrial" (CFAI) → ID: 0a4a5d1b-8ddd-42d1-a449-bfc238001628
3. **Catálogos:**
   - Catálogo "Estados" (STATES) → ID: 8f35d073-b1a2-4ced-9765-c5ecaf9bf568
   - Detalle "Activo" (ACTIVE) → ID: 9a782132-3656-47b3-b6b0-4b47c6ffacc2
4. **Reporte:** Consulta exitosa a `/reports/active-catalog` retornando el catálogo creado

Todos los endpoints respondieron con status codes correctos (201 para creaciones, 200 para consultas).

---

### Paso 11 — Verificar observabilidad (OTel)

Con el collector OTel activo (Paso 2) y `OTEL_EXPORTER_OTLP_ENDPOINT`
configurado (Paso 8):

1. Abrir Grafana: <http://localhost:3000>
2. Ir a **Explore** → seleccionar fuente **Tempo**
3. Buscar por `service.name=reference-data-service`
4. Verificar que existen trazas con spans para:
   - HTTP request (método, ruta, status)
   - Query PostgreSQL (si se ejecutó una operación de BD)
5. Verificar que el header `X-Request-Id` de la respuesta coincide con el
   `correlation_id` del span.

> **Nota:** En modo DEV sin configuración OTel, las trazas no se exportan pero
> el servicio funciona correctamente. Para ver trazas completas, configura
> `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` y asegúrate que el
> collector OTel esté corriendo.

---

### Variables de entorno — resumen

| Variable                              | Valor                                              | Requerido |
| ------------------------------------- | -------------------------------------------------- | --------- |
| `REFERENCE_DATA_DATABASE_URL`         | `postgres://design_software_user:change-me@localhost:15432/design-software-develop?sslmode=disable` | Sí |
| `IAM_JWT_ISSUER`                      | `https://iam.it.sena.local`                        | Sí        |
| `IAM_JWKS_URL`                        | `http://localhost:8081/api/v1/auth/.well-known/jwks.json` | Sí |
| `PORT`                                | `8080`                                             | No (default: 8080) |
| `OTEL_EXPORTER_OTLP_ENDPOINT`        | `http://localhost:4318`                            | No (sin collector → noop) |
| `OTEL_EXPORTER_OTLP_INSECURE`        | `true`                                             | No        |
| `REFERENCE_DATA_AMQP_URL`            | `amqp://app:app@localhost:5672/`                   | No (sin URL → stdout) |

---

### Publicación confiable de eventos (HU-REF-019)

Con `REFERENCE_DATA_AMQP_URL` configurada, el publicador envía eventos al topic
exchange durable `reference-data-events` con `mandatory=true`, mensajes
persistentes y publisher confirms. Los mensajes no enrutados se detectan con
`basic.return`; la publicación reintenta con esperas de 1, 5 y 30 segundos
(cuatro intentos en total). Si sigue fallando, se publica en el exchange/cola
durable `reference-data-events.dlq`, enlazado con `#`, con retención de siete
días. Si la publicación a DLQ también falla, el error se devuelve al llamador.

Para validar broker, JWT real y features de IAM sin exponer passwords en la
terminal ni usar ejecutables Go bloqueados por Windows, ejecutar desde la raíz:

```powershell
.\scripts\run-hu-ref-019-e2e.ps1
```

El runner solo resetea los dos usuarios `.test` dentro del PostgreSQL etiquetado
`hu019-e2e`; se niega a continuar si el proyecto/servicio Docker no coincide.
Genera una contraseña efímera, ejecuta el test y elimina los archivos
temporales. No activa Outbox: la consistencia transaccional fuerte se gestiona
como HU específica, según la decisión de arquitectura.

---

### Endpoints del servicio

Las pruebas de integración con PostgreSQL usan `REFERENCE_DATA_TEST_DSN` y
requieren una base migrada con `reference-data-db`. IAM emite JWT RS256;
`reference-data` valida la firma mediante JWKS y autoriza por los features RBAC
del claim `features`. Los health checks son públicos y las rutas funcionales
requieren JWT. El filtrado específico de datos por `scope_type` queda fuera de
HU-REF-015 hasta definir reglas por recurso.

| Método | Ruta                                    | Feature requerida      |
| ------ | --------------------------------------- | ---------------------- |
| GET    | `/health/live`, `/health`               | —                      |
| GET    | `/health/ready`                         | —                      |
| GET    | `/macroregions`                         | `REF_HIERARCHY_VIEW`   |
| GET    | `/macroregions/{id}`                    | `REF_HIERARCHY_VIEW`   |
| POST   | `/macroregions`                         | `REF_HIERARCHY_MANAGE` |
| PUT    | `/macroregions/{id}`                    | `REF_HIERARCHY_MANAGE` |
| DELETE | `/macroregions/{id}`                    | `REF_HIERARCHY_MANAGE` |
| GET    | `/microregions`, `/microregions/{id}`   | `REF_HIERARCHY_VIEW`   |
| POST   | `/microregions`                         | `REF_HIERARCHY_MANAGE` |
| GET    | `/departments`, `/departments/{id}`     | `REF_HIERARCHY_VIEW`   |
| POST   | `/departments`                          | `REF_HIERARCHY_MANAGE` |
| GET    | `/municipalities`, `/municipalities/{id}` | `REF_HIERARCHY_VIEW` |
| POST   | `/municipalities`                       | `REF_HIERARCHY_MANAGE` |
| GET    | `/training-centers`, `/training-centers/{id}` | `REF_HIERARCHY_VIEW` |
| POST   | `/training-centers`                     | `REF_HIERARCHY_MANAGE` |
| GET    | `/institutional-units`, `/institutional-units/{id}` | `REF_HIERARCHY_VIEW` |
| POST   | `/institutional-units`                  | `REF_HIERARCHY_MANAGE` |
| GET    | `/catalogs`, `/catalogs/{id}`           | `REF_CATALOG_VIEW`     |
| POST   | `/catalogs`                             | `REF_CATALOG_MANAGE`   |
| GET    | `/catalogs/{catalog_id}/details`        | `REF_CATALOG_VIEW`     |
| POST   | `/catalogs/{catalog_id}/details`        | `REF_CATALOG_MANAGE`   |
| GET    | `/parameters`, `/parameters/{id}`       | `REF_PARAMETER_VIEW`   |
| POST   | `/parameters`                           | `REF_PARAMETER_MANAGE` |
| GET    | `/reports/active-catalog`               | `REF_CATALOG_VIEW`     |

---

### Solución de problemas

#### El servicio no inicia: `REFERENCE_DATA_DATABASE_URL is required`

Verificar que la variable de entorno está configurada:

```powershell
echo $env:REFERENCE_DATA_DATABASE_URL
```

#### Error 503 `DEPENDENCY_UNAVAILABLE` al llamar endpoints protegidos

El servicio no puede alcanzar a IAM para validar el JWKS. Verificar:

```powershell
curl.exe http://localhost:8081/api/v1/auth/.well-known/jwks.json
```

Debe devolver un JSON con las claves públicas RSA.

#### Error 401 `UNAUTHENTICATED`

El JWT es inválido o expiró. Obtener un nuevo token:

```powershell
curl.exe -X POST http://localhost:8081/api/v1/auth/login `
  -H "Content-Type: application/json" `
  -d '{"email":"admin@sena.edu.co","password":"Test@2026!"}'
```

#### Error 403 `FORBIDDEN`

El JWT no contiene la feature requerida. Verificar que el usuario tiene el rol
`SYSTEM_ADMIN` asignado:

```powershell
curl.exe http://localhost:8081/api/v1/auth/me `
  -H "Authorization: Bearer $TOKEN"
```

La lista `features` debe incluir `REF_HIERARCHY_MANAGE:GLOBAL`.

#### Error 409 `CONFLICT` al crear un recurso

El código o DANE code ya existe. Usar valores diferentes o eliminar el recurso
existente con `DELETE /{resource}/{id}`.

#### PostgreSQL: `SET LOCAL ROLE reference_writer` falla

Los roles no fueron creados por Liquibase. Verificar que las migraciones DCL
corrieron correctamente:

```powershell
cd C:\reference-data\docker-infra\design-software-docker-infra
docker compose exec postgres psql -U design_software_user -d design-software-develop -c "SELECT rolname FROM pg_roles WHERE rolname LIKE 'reference_%';"
```

Resultado esperado: `reference_reader`, `reference_writer`, `reference_admin`.
Si no aparecen, re-ejecutar `docker compose --profile tooling run --rm liquibase-reference update`.

#### RabbitMQ: eventos no se publican

Verificar conectividad:

```powershell
curl.exe -u app:app http://localhost:15672/api/overview
```

Si falla, verificar que el perfil `broker` está activo:

```powershell
docker compose --profile broker ps
```

---

### Orden de dependencias entre HUs

| HU            | Dependencia                                 |
| ------------- | ------------------------------------------- |
| HU-REF-001    | Seguridad HTTP (JWT/JWKS) — base de auth    |
| HU-REF-003/004 | Escritura de jerarquía — endpoints POST/PUT/DELETE |
| HU-REF-005    | Reporte de catálogo activo                  |
| HU-REF-008    | Publicación de eventos AMQP                 |
| HU-REF-009    | Observabilidad OTel + health checks         |
| **HU-REF-010** | **Levantado local E2E — integra todas las anteriores** |

---

### Criterios de aceptación HU-REF-010

| # | Criterio | Verificación |
|---|----------|--------------|
| E1 | Arranque con un comando | `docker compose --profile tooling run --rm liquibase-reference update` + arranque del servicio contra el mismo Postgres |
| E2 | Flujo E2E real | JWT real de IAM → crear macrorregión → crear centro de formación → consultar reporte |
| E3 | Documentado en README | Pasos reproducibles por cualquier miembro del equipo, con las 4 credenciales/puertos reales de docker-infra |
