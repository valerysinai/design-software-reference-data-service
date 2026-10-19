# HU-REF-020: levantado E2E real entre Reference Data e IAM

## Alcance

Esta entrega valida los criterios E1-E4 de HU-REF-020 con PostgreSQL, IAM y Reference Data reales, sin JWT de prueba ni mocks. Se aprovecha el trabajo histórico agrupado en HU-REF-019 y se añade una ejecución independiente identificada como HU-REF-020.

No incluye el reto R1 ni declara completada la integración del microfrontend dentro del web-shell. Tampoco acredita el estado de cierre o fusión de la HU en la organización original.

## Reproducción

Requisitos: Docker Desktop iniciado, contenedores Linux, PowerShell y acceso a las descargas de Maven, Go e imágenes Docker. Java 21 y Go 1.26.5 se ejecutan dentro de Docker; no es necesario cambiar el JDK de Windows.

Clonar estos repositorios en una carpeta común, con las rutas siguientes:

```text
carpeta-de-trabajo/
  design-software-reference-data-service/             hu-ref-020
  design-software-iam-service/                        develop
  database/design-software-iam-db/                    hu-ref-020
  database/design-software-reference-data-db/         develop
  docker-infra/design-software-docker-infra/          hu-ref-020
```

Desde este repositorio:

```powershell
./scripts/run-hu-ref-020-e2e.ps1
```

El runner de infraestructura genera contraseñas locales aleatorias, levanta la base aislada `hu_ref_020`, aplica ambos changelogs, prepara las fixtures oficiales de IAM y compila el reactor con Java 21. Espera los endpoints reales y ejecuta `TestHURef020ParametersWithRealIAMJWT`. Una configuración incompleta o un resultado omitido no cuenta como PASS.

Los servicios publicados se consultan en:

- IAM: `http://localhost:18082/ready` y `http://localhost:18082/api/v1/auth/.well-known/jwks.json`.
- Reference Data: `http://localhost:18080/health/ready`.
- Base de datos: `localhost:15438`, únicamente para este entorno.

La configuración completa y el procedimiento para detenerlo están en `docker-infra/validation/hu-ref-020/README.md`, dentro de su repositorio correspondiente. Los servicios comparten red local para consultar JWKS por loopback, manteniendo la exigencia existente de HTTPS fuera de localhost.

## Responsabilidad de cada cambio

| Repositorio | Cambio de esta entrega |
|---|---|
| reference-data-service | Entrada de prueba HU-REF-020, configuración requerida, rechazo de firma alterada, comando de ejecución, ADR e informe |
| docker-infra | Entorno aislado, Java 21, reactor Maven completo, migraciones, usuarios de aplicación, runner y evidencia de ejecución |
| iam-db | Preparación de contraseñas de las fixtures de integración y comprobación de campos/roles en una base aislada |
| iam-service | Se ejecuta el código existente; no se atribuyen aquí cambios de IAM a Reference Data |
| reference-data-db | Se ejecutan los changelogs existentes; no se atribuyen migraciones previas a esta entrega |

## Criterios

| Criterio | Comprobación |
|---|---|
| E1: IAM funcionando | Java 21 dentro de Docker, reactor raíz completo, migraciones IAM y Reference Data, validación Hibernate y readiness |
| E2: datos reales de seguridad | Columnas `must_change_password` y `password_temporary_expires_at`, tabla `identity.outbox`, usuarios de fixtures con ADMIN_STAFF y LEARNER |
| E3: token válido | Login HTTP real, issuer/sub/exp, rol y features con alcance GLOBAL esperados, firma verificada al consumir JWKS real |
| E4: autorización real | Admin GET 200, POST 201, PUT 200; LEARNER GET/POST/PUT 403; JWT ausente, mal formado o con firma alterada 401 |

ADMIN_STAFF es la cuenta administradora de esta fixture, con permisos reales de parámetros. Los identificadores y correos `.test` conservan el nombre histórico HU-REF-018 para reutilizar el seed oficial. No son usuarios personales.

## Evidencia y resultado

Consultar el informe `HU-REF-020-RESULTADO.md` que acompaña la ejecución comprobada. Los archivos originales se generan en `docker-infra/validation/hu-ref-020/evidence/`: migraciones, esquema y fixtures, versiones de runtime, E2E y estado de servicios. Las contraseñas, claves y JWT no se incorporan a esos documentos.

El ADR está en [ADR-HU-REF-020-integracion-real-iam.md](adr/ADR-HU-REF-020-integracion-real-iam.md).

## Entrega y revisión

Los tres repositorios modificados requieren commits y revisión en sus propias ramas `hu-ref-020`, hacia `develop`. El PR principal debe enlazar los PR de infraestructura y de fixtures IAM. La aprobación académica y el cierre del ítem original corresponden al instructor; la evidencia local no cambia automáticamente esos estados.
