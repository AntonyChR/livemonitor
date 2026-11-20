# Monitor y Grabador de Directos de Douyin (Versión en Go)

Backend robusto y concurrente en Go para el monitoreo y grabación automática de directos de la plataforma Douyin. Incluye una interfaz gráfica nativa construida con MyGo y un modo CLI sin interfaz para operar como servicio autónomo.

---

## 1. Requisitos del Sistema

- **Sistema Operativo**: Linux (x86_64 o ARM64).
- **Go**: Versión 1.27.3 o superior.
- **GTK 3**: necesario para la interfaz gráfica nativa (`libgtk-3`).
- **libayatana-appindicator3**: necesario para el icono de la bandeja del sistema.
- **Herramientas en `PATH`**:
  - `ffmpeg` (versión 4.x o superior con soporte MP4).
  - `streamlink` (versión 5.x o superior con plugin Douyin).
  - `ffprobe` (necesario para pruebas de integración).
  - `notify-send` y `paplay` (opcionales; utilizados para notificaciones y alertas sonoras de escritorio).

---

## 2. Compilación e Instalación

El proyecto usa [MyGo](https://github.com/egoist/mygo) para la UI nativa. Las dependencias se resuelven con los módulos de Go:

### Compilación rápida con `make`:
```bash
cd go_version
make build
```
El ejecutable resultante se genera en `bin/recorder`.

### Compilación directa con `go`:
```bash
cd go_version
go build -o bin/recorder ./cmd/recorder
```

---

## 3. Configuración

### Archivo de configuración (`settings.json`)
La ruta de grabaciones, la calidad y el archivo de usuarios se guardan como JSON en:

```text
$XDG_CONFIG_HOME/live-recorder/settings.json
# o, si XDG_CONFIG_HOME no está definido:
~/.config/live-recorder/settings.json
```

Ejemplo:

```json
{
  "output_directory": "/home/usuario/solid/videos",
  "quality": "sd1",
  "user_list_file": "/home/usuario/user_list.txt",
  "minimize_to_tray": false
}
```

La precedencia es: valores por defecto, luego `settings.json` y por último los flags de línea de comandos. Los cambios hechos en la pestaña **Configuración** se escriben inmediatamente en este archivo.

### Archivo de usuarios (`user_list.txt`)
El backend lee la lista de usuarios en cada ronda de monitoreo. La ruta por defecto es `~/user_list.txt` y puede cambiarse con `-users` o desde la pestaña **Configuración**. El formato es:

```text
# id alias grabar
7513525489341449273 Gwen1 true
3977409713946135 Gwen2 true
110404486053507 Gwen3 false
```

- Cada línea debe contener exactamente tres campos: `id alias true|false`.
- El `id` debe contener únicamente dígitos numéricos.
- El `alias` no admite espacios en blanco (se permiten guiones y caracteres alfanuméricos).
- Se admiten comentarios con `#`, líneas vacías y tabulaciones.
- Si se detecta un ID duplicado o formato incorrecto al inicio, el programa se detiene. Durante la ejecución, se conserva la última configuración válida y se notifica la advertencia.
- Cambiar a `false` o eliminar un usuario evita nuevas grabaciones, pero **no** interrumpe las que estén actualmente en curso.

---

## 4. Ejecución

### Modos de ejecución

La aplicación arranca por defecto en modo gráfico (`ui`):

```bash
./bin/recorder -users ./user_list.txt
```

Para ejecutar sin interfaz, igual que el backend original:

```bash
./bin/recorder -mode cli -users ./user_list.txt
```

Al marcar «Minimizar al cerrar», cerrar la ventana oculta la aplicación en la bandeja del sistema; desde su menú se puede volver a abrir o cerrar definitivamente.

En ambos modos se buscará el archivo de usuarios en la ruta indicada o en `~/user_list.txt`, se usará una pausa de 120 segundos entre rondas y las grabaciones se guardarán en `~/solid/videos`. La carpeta, la calidad y el archivo de usuarios se recuerdan en `settings.json`.

La ventana de la UI muestra:
- **Usuarios**: alias, estado de directo, estado de grabación con temporizador `hh:mm:ss`, grabación automática y acciones de iniciar/detener.
- **Configuración**: carpeta de grabaciones, archivo de usuarios, calidad por defecto y opción «Minimizar al cerrar», con sus selectores correspondientes.
- **Barra inferior**: número de usuarios, grabaciones activas, usuarios en directo y acceso a la carpeta de grabaciones.

### Argumentos posicionales (compatibilidad con la versión TypeScript):
```bash
./bin/recorder [intervalo_segundos] [ruta_user_list.txt]
# Ejemplo:
./bin/recorder 60 ./user_list.txt
```

### Opciones por flags:
```bash
./bin/recorder -mode cli -interval 60 -users ./user_list.txt -output /ruta/a/videos -quality sd1 -notify=true
```

| Flag | Descripción | Valor por defecto |
|---|---|---|
| `-mode` | Modo de ejecución: `ui` o `cli` | `ui` |
| `-interval` | Pausa en segundos tras cada ronda de consultas | `120` |
| `-users` | Ruta al archivo `user_list.txt` | `~/user_list.txt` |
| `-output` | Directorio de destino para las grabaciones | `~/solid/videos` |
| `-quality` | Calidad de flujo para Streamlink | `"sd1"` |
| `-notify` | Habilitar notificaciones de escritorio | `true` |

---

## 5. Ejecución de Pruebas

El proyecto cuenta con una completa suite de pruebas unitarias y de integración:

### Ejecutar todas las pruebas:
```bash
make test
# O directamente:
go test -v ./...
```

### Ejecutar con el detector de condiciones de carrera:
```bash
make test-race
# O directamente:
go test -v -race ./...
```

### Análisis estático con `go vet`:
```bash
make vet
```

### Prueba de integración con procesos reales:
La prueba en `tests/integration_test.go` genera un stream sintético mediante FFmpeg lavfi, establece los pipes del sistema operativo, supervisa el progreso `out_time_us`, finaliza ordenadamente la grabación y valida el archivo MP4 mediante `ffprobe`:
```bash
go test -v ./tests/...
```

---

## 6. Diagnóstico y Observabilidad

### 6.1 Logging Estructurado
El backend utiliza la biblioteca estándar `log/slog`. Todos los logs se dirigen estrictamente a **`stderr` (`os.Stderr`)** con nivel estructurado, reservando `stdout` para la futura comunicación IPC:
- Mensajes de inicio y finalización de grabaciones con alias, PID y ruta de salida.
- Errores de consulta etiquetados por usuario.
- Notificaciones de watchdog de inactividad o falta de progreso inicial.

### 6.2 Códigos de Salida y Cierre Limpio
- Ante señales `SIGINT` (Ctrl+C) o `SIGTERM`:
  1. Se cancelan las consultas de monitoreo activas.
  2. Se envía `SIGTERM` al grupo de procesos de Streamlink (`-pid`).
  3. Se espera a que FFmpeg reciba EOF y finalice la estructura MP4 (`moov atom`).
  4. Si se supera el tiempo límite configurado (`FinalizeTimeout`), se escala a `SIGTERM`/`SIGKILL`.
- **Código de salida 0**: Cierre normal y ordenado de todos los grabadores y monitores.
- **Código de salida 1**: Fallo de configuración inicial, ausencia de dependencias en PATH o cierre forzado tras agotar plazos.

---

## 7. Documentación Adicional

Para más detalles técnicos sobre el diseño interno del backend, consultar la carpeta `docs/`:

- [Arquitectura y Ciclos de Vida](docs/architecture.md): Diagramas de componentes, supervisión de procesos, tuberías Unix y ciclo de vida de la aplicación.
- [Modelos de Estado y Contrato de Eventos](docs/state_models_and_events.md): Definición de `UserStatus`, `RecordingState`, `GlobalStatus` y catálogo de eventos fuertemente tipados.
- [Funcionalidades Preservadas y Decisiones Técnicas](docs/preserved_features_and_decisions.md): Comparativa exhaustiva respecto al backend en TypeScript, justificación de decisiones y posibles mejoras.
