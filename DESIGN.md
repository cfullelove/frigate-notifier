# MQTT Frigate Notification Bot — Implementation Design Specification

**Status:** Ready for implementation  
**Target language:** Go  
**Primary configuration:** YAML plus environment-variable expansion  
**Persistence:** None; runtime event correlation is intentionally in memory only

## 1. Purpose

Build a long-running Go service that subscribes to Frigate review events published over MQTT, filters new events using the current state of a Home Assistant alarm entity and configurable rules, uses Gemini image analysis to reject likely false positives, and sends qualifying media to a configured Telegram chat.

The service must:

1. Subscribe to Frigate review events via MQTT.
2. Process only `new` and `end` review message types.
3. Maintain the configured Home Assistant alarm entity's state through the Home Assistant WebSocket API (initial state plus live `state_changed` updates).
4. Evaluate configurable filter rules for `new` messages.
5. For an eligible `new` review:
   - fetch the snapshot for the first linked Frigate event;
   - ask Gemini whether the image contains a person;
   - if it does, send the snapshot and Gemini description to Telegram;
   - otherwise, log the false-positive explanation without notifying.
6. Remember in memory whether the initial notification was sent.
7. For the corresponding `end` review, send a Frigate recording clip only if the initial notification was successfully sent.

## 2. Explicit product decisions

- Runtime state does **not** need to survive process restarts.
- Only the first linked Frigate event is analysed.
- Only one clip is sent, using the first linked event's camera where possible.
- The initial Telegram notification contains both the snapshot and Gemini's description.
- Home Assistant state is subscribed to and cached rather than polled for each MQTT event.
- Filtering is configured in `config.yml`.
- Filtering is performed only when handling `new`; an `end` message must not re-evaluate the alarm state or rules.
- Unknown MQTT message types, including `update`, are ignored.
- If a dependency or state is uncertain, the service fails closed and sends no notification.

## 3. Non-goals

The first implementation does not need:

- persistent event tracking or a database;
- a web UI;
- multiple Telegram destinations;
- analysis of every linked event/camera;
- dynamic configuration reload;
- media transcoding or compression;
- historical recovery after restart;
- Home Assistant polling for every event.

## 4. Terminology and identifiers

A review event contains two distinct identifiers:

- **Review ID:** `message.after.id`, for example `61d418fd-b3bb-4411-bbfe-a2d1a53de514`. Use this as the correlation key between `new` and `end` messages.
- **Linked event ID:** `message.after.linked_events[0].id`, for example `1788251611.071814-qyzj20`. Use this to request a snapshot from Frigate.

A linked event also identifies a camera. Prefer `after.linked_events[0].camera`; if it is empty, fall back to `after.cameras[0]`. The chosen camera is used to request the recording clip.

## 5. Input contract

### 5.1 MQTT topic

Default topic:

```text
frigate_custom_reviews/reviews
```

The topic must be configurable.

### 5.2 MQTT payload

The Go MQTT client receives the MQTT payload itself, not the n8n envelope. The expected payload resembles:

```json
{
  "type": "new",
  "before": null,
  "after": {
    "id": "61d418fd-b3bb-4411-bbfe-a2d1a53de514",
    "profile_name": "Outside",
    "state": "active",
    "start_time": 1788251611.071814,
    "event_count": 1,
    "active_events": 1,
    "linked_events": [
      {
        "id": "1788251611.071814-qyzj20",
        "camera": "Deck"
      }
    ],
    "objects": ["person"],
    "cameras": ["Deck"],
    "zones": []
  }
}
```

An `end` payload resembles:

```json
{
  "type": "end",
  "before": {
    "id": "61d418fd-b3bb-4411-bbfe-a2d1a53de514",
    "profile_name": "Outside",
    "state": "pending-close",
    "start_time": 1788251611.071814,
    "end_time": 1788251612.008113,
    "linked_events": [
      {
        "id": "1788251611.071814-qyzj20",
        "camera": "Deck"
      }
    ],
    "cameras": ["Deck"]
  },
  "after": {
    "id": "61d418fd-b3bb-4411-bbfe-a2d1a53de514",
    "profile_name": "Outside",
    "state": "ended",
    "start_time": 1788251611.071814,
    "end_time": 1788251612.008113,
    "linked_events": [
      {
        "id": "1788251611.071814-qyzj20",
        "camera": "Deck"
      }
    ],
    "cameras": ["Deck"]
  }
}
```

The n8n examples wrap this payload under `message` and add `topic`. The service should not require that wrapper. It is acceptable, but not required, to support both forms for migration convenience; if both are supported, tests must cover both and the raw MQTT shape remains canonical.

### 5.3 Go model guidance

Use explicit models for fields the service consumes and tolerate unknown JSON fields. Frigate timestamps are fractional Unix seconds, so represent them without losing precision. Suitable options include `json.Number` retained for URL construction or `float64` formatted with `strconv.FormatFloat(value, 'f', -1, 64)`.

Minimum fields:

```go
type ReviewMessage struct {
    Type   string      `json:"type"`
    Before *ReviewData `json:"before"`
    After  *ReviewData `json:"after"`
}

type ReviewData struct {
    ID           string        `json:"id"`
    ProfileName  string        `json:"profile_name"`
    State        string        `json:"state"`
    StartTime    json.Number   `json:"start_time"`
    EndTime      json.Number   `json:"end_time"`
    LinkedEvents []LinkedEvent `json:"linked_events"`
    Objects      []string      `json:"objects"`
    Cameras      []string      `json:"cameras"`
    Zones        []string      `json:"zones"`
}

type LinkedEvent struct {
    ID     string `json:"id"`
    Camera string `json:"camera"`
}
```

Because a missing number and zero should be distinguishable, implementation may use a custom optional number type or validate raw JSON presence.

## 6. Configuration

### 6.1 Example `config.yml`

```yaml
mqtt:
  broker: tcp://192.168.1.10:1883
  client_id: frigate-notifier
  username: "${MQTT_USERNAME}"
  password: "${MQTT_PASSWORD}"
  topic: frigate_custom_reviews/reviews
  qos: 1
  connect_timeout: 10s
  reconnect_delay: 5s

home_assistant:
  url: http://homeassistant.local:8123
  token: "${HOME_ASSISTANT_TOKEN}"
  alarm_entity_id: alarm_control_panel.home
  connect_timeout: 10s
  reconnect_delay: 5s
  state_max_age: 5m
  state_refresh_interval: 1m

frigate:
  base_url: http://frigate.local:5000
  # Optional bearer token or other authentication should be supported if practical.
  token: "${FRIGATE_TOKEN}"
  request_timeout: 30s
  snapshot:
    retries: 3
    retry_delay: 1s
    quality: 90
  clip:
    retries: 5
    retry_delay: 2s
    timeout: 2m

# Select and document the currently supported stable Gemini model during implementation.
# Do not silently hard-code a model different from this configured value.
gemini:
  api_key: "${GEMINI_API_KEY}"
  model: gemini-2.5-flash
  timeout: 30s
  prompt: |
    Return a valid JSON object with the following format:

    {
       "has_person": bool,
       "description": string
    }

    Populate the object with the appropriate values based on the provided image.

    If there is no person, add a humorous explanation as to why the classification might have failed.

    Respond ONLY with the strictly raw JSON object. NO Markdown Code Blocks

telegram:
  bot_token: "${TELEGRAM_BOT_TOKEN}"
  chat_id: "${TELEGRAM_CHAT_ID}"
  timeout: 2m

filter:
  default_action: deny
  rules:
    - name: Armed away
      action: allow
      alarm_states:
        - armed_away

    - name: Armed at night
      action: allow
      alarm_states:
        - armed_night

    - name: Car port while armed home
      action: allow
      alarm_states:
        - armed_home
      profile_names:
        - Car Port

processing:
  workers: 4
  queue_size: 100
  event_ttl: 24h
  shutdown_timeout: 30s

logging:
  level: info
  format: json
```

### 6.2 Environment expansion

Expand `${NAME}` references after reading YAML and before unmarshalling/validation. A referenced but unset variable must cause startup failure when the resulting field is required. Never print secret values in validation errors or logs.

### 6.3 Validation

Fail startup with actionable errors for at least:

- missing MQTT broker, topic or client ID;
- MQTT QoS outside 0–2;
- missing Home Assistant URL, token or alarm entity ID;
- invalid or non-positive durations;
- missing Frigate base URL;
- invalid snapshot quality if supplied;
- missing Gemini key, model or prompt;
- missing Telegram token or chat ID;
- invalid filter actions/default action;
- duplicate or unnamed rules, if names are required by the implementation;
- non-positive worker or queue counts.

Normalise base URLs by removing trailing slashes. Do not log tokens.

## 7. Filter semantics

Rules are ordered and **first match wins**.

Allowed actions:

- `allow`
- `deny`

Allowed default actions are the same. Each rule may contain:

- `alarm_states`
- `profile_names`

Semantics:

- Conditions within a rule use AND logic.
- Values within one condition use OR logic.
- A missing condition imposes no restriction.
- Matching should be exact and case-sensitive unless a future configuration option explicitly changes it.
- The first matching rule supplies its action.
- If no rule matches, use `default_action`.

Example outcomes for the supplied configuration:

| Alarm state | Profile | Result |
|---|---|---|
| `armed_away` | Any | Allow |
| `armed_night` | Any | Allow |
| `armed_home` | `Car Port` | Allow |
| `armed_home` | `Outside` | Deny |
| `disarmed` | Any | Deny |
| unavailable/stale | Any | Deny before rule evaluation |

An empty condition list should be rejected during configuration validation rather than interpreted ambiguously. A rule with no conditions may be permitted as an explicit catch-all, but this behaviour must be documented and tested; alternatively reject conditionless rules and rely on `default_action`.

Log the matched rule name and final action at structured debug/info level without secrets.

## 8. Home Assistant integration

### 8.1 Transport

Use the Home Assistant WebSocket API:

```text
ws://host:8123/api/websocket
wss://host/api/websocket
```

Derive the WebSocket URL from the configured HTTP(S) URL unless a dedicated WebSocket URL is added to configuration.

### 8.2 Connection lifecycle

For every connection:

1. Connect with timeout.
2. Receive `auth_required`.
3. Send:

   ```json
   {"type":"auth","access_token":"..."}
   ```

4. Require `auth_ok`; treat `auth_invalid` as an error.
5. Obtain initial alarm state. A straightforward method is `get_states` and selecting the configured entity. A more targeted supported command may be used if available.
6. Subscribe to `state_changed` events, optionally filtering server-side when supported; otherwise filter received events client-side by `entity_id`.
7. Refresh the current configured entity state at `state_refresh_interval` using the established WebSocket connection, even when it has not changed. The interval must be shorter than `state_max_age`.
8. Update the in-memory alarm cache only for the configured entity.
9. Reconnect with bounded delay/back-off after disconnection.

Each command must use a unique numeric message ID and its result must be correlated correctly. The read loop should have one owner to avoid concurrent WebSocket reads.

### 8.3 Alarm state cache

Suggested representation:

```go
type AlarmState struct {
    State     string
    UpdatedAt time.Time
    Available bool
}
```

Access must be concurrency-safe. `Available` is false when:

- no initial state has been obtained;
- the configured entity does not exist;
- its state is `unknown` or `unavailable`;
- cached state age exceeds `state_max_age`.

The system must fail closed if alarm state is unavailable or stale. A Home Assistant disconnect does not immediately invalidate the most recently received state, but it becomes unusable after `state_max_age`.

Use local receipt time for freshness; do not rely solely on remote timestamps or clock synchronisation.

## 9. Frigate integration

### 9.1 Snapshot

Request:

```text
GET {base_url}/api/events/{linked_event_id}/snapshot.jpg?quality={quality}
```

The documented endpoint is:

```text
/api/events/:event_id/snapshot.jpg
```

The `quality` parameter is optional; include it when configured. URL-escape the event ID as a path segment. Require a successful 2xx response and an image content type. Apply configured timeout, bounded retries and delay/back-off to transient failures.

Snapshot retries are useful because media may not be ready immediately after a `new` message. Retry transient network errors, HTTP 408, 425, 429 and 5xx responses. Generally do not retry permanent 4xx errors. Limit response size to a sensible configurable or documented maximum to prevent unbounded memory use.

### 9.2 Clip

Request:

```text
GET {base_url}/api/{camera_name}/start/{start_ts}/end/{end_ts}/clip.mp4
```

Use:

- camera: first linked event camera, falling back to `after.cameras[0]`;
- start: the review start time;
- end: the completed review end time.

URL-escape the camera name as a path segment. Preserve timestamp precision and do not convert fractional seconds to integers.

Download clips to a securely created temporary file rather than holding the entire video in memory. Enforce an HTTP response-size limit. Close and delete the file on every success or failure path. Require a successful 2xx response and an expected video/octet-stream content type, allowing for real Frigate deployments that use a generic binary content type.

Retry transient failures because the final recording may not be available immediately after `end`.

### 9.3 Authentication

The client should be structured so optional Frigate authentication headers can be added. If bearer-token support is implemented, omit the header when the token is blank. Never log the token.

## 10. Gemini integration

### 10.1 Request

Send the snapshot and configured prompt to the configured Gemini model. Prefer Google's supported Go SDK at implementation time rather than hand-rolling an obsolete endpoint.

Where the selected model/API supports it, request structured JSON output using:

- response MIME type `application/json`;
- a schema requiring:
  - `has_person`: boolean;
  - `description`: string.

Do not assume the model will obey the prompt; validate locally.

### 10.2 Required result

```go
type AnalysisResult struct {
    HasPerson  bool   `json:"has_person"`
    Description string `json:"description"`
}
```

Validation rules:

- response must contain exactly one usable JSON object after SDK response extraction;
- `has_person` must be present and boolean;
- `description` must be present and non-empty;
- surrounding Markdown fences are not accepted as valid strict output unless the SDK's structured-output layer has already normalised them;
- enforce a reasonable maximum description length before logging or sending it.

If analysis is invalid, times out or fails after retries, log an analysis error and send no Telegram notification. This is an operational failure, not a confirmed false positive.

If `has_person` is false, log the description as the false-positive reason and do not notify.

If `has_person` is true, use the description in the Telegram snapshot caption.

### 10.3 Prompt

The default prompt must be exactly configurable and should initially be:

```text
Return a valid JSON object with the following format:

{
   "has_person": bool,
   "description": string
}

Populate the object with the appropriate values based on the provided image.

If there is no person, add a humorous explanation as to why the classification might have failed.

Respond ONLY with the strictly raw JSON object. NO Markdown Code Blocks
```

## 11. Telegram integration

Use the Telegram Bot API.

### 11.1 Initial notification

Call `sendPhoto` with:

- configured `chat_id`;
- downloaded snapshot bytes/file;
- Gemini description as caption.

A successful Telegram API result is the boundary for marking the review as notified. Store the returned Telegram message ID if useful for clip replies, although replying is optional.

Respect Telegram caption limits. If the description exceeds the supported limit, truncate safely by Unicode code points and indicate truncation, or send a short caption followed by a text message. The simpler preferred first implementation is safe caption truncation.

### 11.2 End clip

Call `sendVideo` with:

- configured `chat_id`;
- the temporary MP4 file;
- an optional concise caption identifying the camera/profile;
- optionally `reply_to_message_id` pointing to the initial photo if supported and desired.

If Telegram rejects the clip because of its upload size, log a structured warning/error. Media compression and link fallback are non-goals for the initial version. The client should detect limits early where possible and avoid an upload known to be impossible.

A Telegram response is successful only when both HTTP status and Telegram's JSON `ok` result indicate success.

### 11.3 Idempotency

Within a single process lifetime:

- do not send more than one initial photo for a review ID;
- do not send more than one clip for a review ID;
- MQTT duplicate delivery must not produce duplicate Telegram messages.

Perfect exactly-once behaviour across crashes is explicitly not required because state is not persisted.

## 12. Event processing and concurrency

### 12.1 MQTT callback

The MQTT callback must be lightweight:

1. Copy message topic/payload as needed.
2. Submit it to a bounded internal queue.
3. Return promptly.

Do not perform Frigate, Gemini, Home Assistant or Telegram network operations inside the callback.

If the queue is full, fail closed: drop the incoming message and emit a structured error/metric. Do not block indefinitely in the MQTT library callback.

### 12.2 Per-review ordering

General worker concurrency is allowed, but operations for one review ID must be serialised. An `end` message can arrive while its `new` message is still being analysed.

Suggested statuses:

```go
type EventStatus string

const (
    EventProcessing EventStatus = "processing"
    EventIgnored    EventStatus = "ignored"
    EventNotified   EventStatus = "notified"
    EventClipSending EventStatus = "clip_sending"
    EventClipSent   EventStatus = "clip_sent"
    EventFailed     EventStatus = "failed"
)
```

Suggested tracked record:

```go
type TrackedEvent struct {
    Status            EventStatus
    CreatedAt         time.Time
    UpdatedAt         time.Time
    PendingEnd        *ReviewData
    TelegramMessageID int64
}
```

The exact structure may vary, but all access must be race-free.

### 12.3 State transitions

For a valid first `new` message:

```text
absent -> processing
processing -> ignored       (filter denied or Gemini says no person)
processing -> notified      (Telegram photo succeeds)
processing -> failed        (unrecoverable dependency/validation error)
```

If `end` arrives while status is `processing`, store its data as `PendingEnd`. Once photo processing completes:

- `notified` plus pending end -> begin clip sending;
- `ignored` or `failed` plus pending end -> discard pending end without notification.

For an `end` after notification:

```text
notified -> clip_sending -> clip_sent
notified -> clip_sending -> notified/failed-clip
```

The implementation must define retry behaviour without allowing repeated duplicate uploads. A practical approach is to perform bounded clip retries in one processing attempt and then mark clip delivery failed. Do not repeatedly retry forever on later duplicate MQTT `end` deliveries unless the state model explicitly guarantees no duplicate send.

Duplicate handling:

- duplicate `new` while any record exists: ignore;
- duplicate `end` while clip is sending/sent: ignore;
- `end` with no tracked `new`: ignore and log at debug level;
- `new` after a completed/ignored record still within TTL: ignore.

### 12.4 Tracker cleanup

Run periodic cleanup and remove entries older than `processing.event_ttl`. Cleanup must not remove an entry actively processing unless the operation has clearly exceeded its context deadline and is abandoned. A 24-hour default is sufficient.

## 13. Detailed workflows

### 13.1 `new`

1. Decode JSON.
2. If type is not `new` or `end`, log debug and stop.
3. Validate `after` exists, review ID exists, first linked event exists and linked event ID exists.
4. Atomically create the review tracker record as `processing`; if already present, treat as duplicate.
5. Read cached alarm state.
6. If unavailable or stale, mark ignored/failed-closed and stop.
7. Evaluate ordered filter rules using alarm state and `after.profile_name`.
8. If denied, mark ignored and stop.
9. Resolve linked event ID and camera.
10. Fetch snapshot with bounded retry.
11. Submit image to Gemini.
12. Parse and validate structured response.
13. If `has_person == false`, log `false_positive` with reason and mark ignored.
14. If `has_person == true`, send snapshot and description using Telegram `sendPhoto`.
15. Only after Telegram confirms success, mark notified and retain message ID.
16. If a pending `end` is attached, enqueue or invoke clip processing exactly once.

### 13.2 `end`

1. Decode and validate `after`, review ID, start time and end time.
2. Look up the review ID.
3. If absent, ignore; a pre-restart `new` cannot be recovered by design.
4. If processing, retain the latest valid end payload as pending and return.
5. If ignored/failed, ignore.
6. If notified, atomically transition to clip-sending.
7. Resolve camera from the first linked event, falling back to first `cameras` entry. If the end message omits needed values, the tracker may retain the relevant values from `new` as fallback.
8. Fetch clip to a temporary file with bounded retries.
9. Send the clip via Telegram.
10. Mark clip sent on confirmed success, otherwise record a terminal clip error.
11. Leave the entry until TTL cleanup so duplicate messages remain suppressed.

The alarm state and current rules must not be consulted during `end` processing.

## 14. Error handling and retry policy

Categorise errors:

- **Invalid input/configuration:** no retry.
- **Authentication/authorisation failures:** no rapid retry; log without credentials. Connection-level components may continue configured reconnect back-off.
- **Network timeout/reset:** bounded retry with context.
- **HTTP 408, 425, 429, 5xx:** bounded retry; honour `Retry-After` where practical.
- **Other HTTP 4xx:** generally no retry.
- **Gemini safety refusal/invalid JSON:** no Telegram notification; log a sanitised reason.
- **Telegram API `ok: false`:** classify using status/error code; bounded retry only for transient failures.

Use exponential back-off with jitter where practical; the simple configured delay may be the initial delay. Every operation must honour context cancellation and an overall timeout.

## 15. Logging and observability

Use Go structured logging, preferably the standard `log/slog`, with JSON and text format options.

Common fields:

- `component`
- `review_id`
- `linked_event_id`
- `message_type`
- `profile_name`
- `camera`
- `alarm_state`
- `filter_rule`
- `filter_action`
- `attempt`
- `duration_ms`
- `error`

Important events:

- service startup and validated configuration summary (without secrets);
- MQTT connected/disconnected/subscribed;
- Home Assistant authenticated, initial state loaded, state changed, stale state;
- malformed/ignored MQTT messages;
- filter decision and matching rule;
- Frigate media fetch result;
- Gemini result (`has_person` and sanitised description/reason);
- Telegram photo/clip result;
- queue overflow;
- event tracker cleanup;
- graceful shutdown.

Never log:

- API tokens/passwords;
- complete authentication headers;
- snapshot or video bytes;
- full Gemini/Telegram request bodies when they contain media or secrets.

Metrics and health endpoints are optional for the initial version. Keep component boundaries suitable for adding them later.

## 16. Security requirements

- Secrets come from environment variables referenced by YAML, not committed literal values.
- Include a safe `config.example.yml` containing placeholders only.
- Add `.env`, real configuration files and credentials to `.gitignore` where appropriate.
- Use TLS (`mqtts`, HTTPS/WSS) where deployment supports it.
- Support normal system CA verification; any insecure TLS option must be explicit, default false and clearly warned against.
- Bound all downloaded/uploaded media sizes.
- Escape URL path segments; never concatenate untrusted camera/ID values without escaping.
- Create temporary files with restrictive permissions and remove them promptly.
- Apply timeouts to every network operation.

## 17. Graceful startup and shutdown

### Startup order

Recommended:

1. Load, expand and validate configuration.
2. Initialise logger.
3. Initialise HTTP/API clients and in-memory tracker.
4. Start Home Assistant connection and obtain initial alarm state.
5. Start worker pool and cleanup loop.
6. Connect and subscribe to MQTT.

The service may subscribe before Home Assistant is ready only if it reliably fails closed until state becomes available. Prefer waiting for an initial state with a bounded startup timeout, then either continue in degraded fail-closed mode or fail startup; document the chosen behaviour. Recommended: continue running in degraded mode so reconnect can recover without an external restart.

### Shutdown

On SIGINT/SIGTERM:

1. Stop accepting/enqueuing MQTT messages and disconnect cleanly.
2. Cancel root context.
3. Stop Home Assistant subscription/reconnect loop.
4. Drain or cancel workers within `shutdown_timeout`.
5. Delete temporary files.
6. Exit non-zero only for abnormal/fatal shutdown conditions.

## 18. Suggested architecture

```text
cmd/frigate-notifier/main.go

internal/config/
    config.go
    config_test.go

internal/mqttclient/
    subscriber.go
    payload.go
    payload_test.go

internal/homeassistant/
    client.go
    protocol.go
    state.go
    client_test.go

internal/filter/
    filter.go
    filter_test.go

internal/frigate/
    client.go
    client_test.go

internal/gemini/
    client.go
    response.go
    response_test.go

internal/telegram/
    client.go
    client_test.go

internal/events/
    processor.go
    tracker.go
    processor_test.go
    tracker_test.go
```

Package naming may change to avoid collisions or improve idiomatic Go. Avoid a generic `utils` package.

### 18.1 Interfaces

Use small interfaces to make orchestration testable:

```go
type AlarmStateProvider interface {
    Current() AlarmState
}

type FrigateClient interface {
    Snapshot(ctx context.Context, eventID string) ([]byte, string, error)
    Clip(ctx context.Context, camera, start, end string) (LocalMedia, error)
}

type ImageAnalyser interface {
    Analyse(ctx context.Context, image []byte, mimeType string) (AnalysisResult, error)
}

type Notifier interface {
    SendPhoto(ctx context.Context, image []byte, mimeType, caption string) (messageID int64, err error)
    SendVideo(ctx context.Context, media LocalMedia, caption string, replyTo int64) (messageID int64, err error)
}
```

A `LocalMedia` abstraction should expose a path/name/content type/size and a safe cleanup mechanism. Avoid interfaces broader than callers need.

## 19. Testing requirements

### 19.1 Unit tests

At minimum, test:

**Configuration**

- environment expansion;
- missing required secret;
- duration parsing;
- invalid QoS/action/default values;
- no secret leakage in errors.

**MQTT parsing**

- valid `new`;
- valid `end`;
- unknown/update type;
- malformed JSON;
- missing `after` or IDs;
- extra unknown fields;
- fractional timestamps preserved.

**Filter**

- armed away allows any profile;
- armed night allows any profile;
- armed home allows `Car Port`;
- armed home denies other profiles;
- disarmed denies;
- first-match-wins including an earlier deny;
- default action;
- stale/unavailable alarm fails closed outside or before filter evaluation.

**Gemini parsing**

- valid person result;
- valid false-positive result;
- missing fields;
- wrong field types;
- empty/overlong description;
- Markdown-fenced/non-JSON output rejected.

**Tracker/state machine**

- duplicate new suppressed;
- end-before-analysis-completes retained;
- notified pending end triggers exactly one clip;
- ignored pending end does not trigger clip;
- duplicate end suppressed;
- TTL cleanup;
- race tests pass.

### 19.2 HTTP/API client tests

Use `httptest.Server` or mocks to test:

- Frigate URL construction and escaping;
- snapshot retry then success;
- clip retry and temporary-file cleanup;
- response size limits;
- Telegram multipart requests and API `ok` handling;
- transient/permanent HTTP error classification.

Mock the Gemini SDK behind the analyser interface if direct HTTP testing is impractical.

### 19.3 Processor integration-style tests

With fake interfaces, cover complete flows:

1. Allowed + person -> photo sent -> end -> clip sent.
2. Allowed + no person -> reason logged -> no photo/clip.
3. Filter denied -> no external media/AI calls.
4. Alarm unavailable/stale -> fail closed.
5. Gemini error -> no notification.
6. Telegram photo failure -> no clip on end.
7. End arrives while Gemini is running -> photo then one clip.
8. Duplicate MQTT deliveries -> one photo and one clip.

Run:

```bash
go test ./...
go test -race ./...
go vet ./...
```

A lint configuration is recommended but optional.

## 20. Deployment artefacts

The implementation should deliver:

- `go.mod` and `go.sum`;
- application source and tests;
- `config.example.yml`;
- `README.md` with configuration, Home Assistant token setup, Telegram bot/chat setup, Gemini key setup, Frigate/MQTT assumptions and run instructions;
- multi-stage `Dockerfile` producing a small non-root runtime image;
- `.dockerignore` and `.gitignore`;
- optional `docker-compose.example.yml`;
- licence only if requested/selected by the project owner.

Example commands:

```bash
go run ./cmd/frigate-notifier -config ./config.yml
```

```bash
docker run --rm \
  -v "$PWD/config.yml:/app/config.yml:ro" \
  --env-file .env \
  frigate-notifier:latest \
  -config /app/config.yml
```

The container should run as a non-root user and have a writable temporary directory for clips.

## 21. Acceptance criteria

Implementation is complete when all of the following are true:

1. The service builds and starts from a YAML configuration with environment-expanded secrets.
2. It subscribes to the configured MQTT topic and ignores all message types except `new` and `end`.
3. It authenticates to Home Assistant over WebSocket, gets the initial configured alarm state and tracks changes.
4. It fails closed when alarm state is missing, unknown, unavailable or stale.
5. Ordered configurable rules reproduce the required behaviour:
   - `armed_away`: allow;
   - `armed_night`: allow;
   - `armed_home` + `Car Port`: allow;
   - otherwise: deny.
6. For an allowed `new`, it fetches the first linked event snapshot and asks Gemini for strict structured analysis.
7. If Gemini returns `has_person: false`, it logs the reason and sends nothing to Telegram.
8. If Gemini returns `has_person: true`, it sends exactly one snapshot with description to the configured Telegram chat.
9. It records notification status in memory by review ID.
10. For the corresponding `end`, it sends exactly one Frigate MP4 clip only when the initial photo was confirmed sent.
11. It correctly handles `end` arriving while `new` is still processing.
12. Duplicate MQTT deliveries do not create duplicate Telegram messages within one process lifetime.
13. Retries are bounded, all network calls have timeouts and media sizes are bounded.
14. Logs are structured and contain no credentials or media content.
15. SIGINT/SIGTERM triggers bounded graceful shutdown and temporary files are cleaned up.
16. Unit/integration-style tests cover the principal successful, denied, false-positive, failure, duplicate and race flows.
17. `go test -race ./...` passes.

## 22. Implementation notes and permitted judgement calls

The coding agent may choose maintained libraries for MQTT, WebSockets, YAML, Telegram and Gemini, subject to these constraints:

- prefer well-maintained, idiomatic Go dependencies;
- pin dependency versions in `go.mod`/`go.sum`;
- do not make business logic dependent on SDK-specific models;
- isolate external services behind small interfaces;
- use the standard library where it is clear and maintainable;
- document any deviations from this specification.

Before finalising the Gemini client, verify the currently supported Go SDK, model identifier and structured-output API. The example model is configuration, not permission to hard-code an obsolete API. Likewise, verify current Telegram media limits and document the enforced values.

When details are absent, choose the simplest fail-closed behaviour that avoids sending a false notification or duplicate media.
