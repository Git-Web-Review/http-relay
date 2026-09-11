# http-relay

HTTP relay for [git-web-review](https://github.com/Git-Web-Review). It
subscribes to the Redis notification channel and POSTs each event, as JSON, to
the webhook URL the recipient configured in their settings.

It is the sibling of `irc-relay` and `email-relay`: same Redis event, same
per-user enable switch and per-category preferences, a different transport.
Unlike those two it does no formatting — there is no template and no locale
handling here. It relays Redis to HTTP and keeps the content as JSON; turning
that into a sentence is the receiving end's job.

## How it works

1. The backend writes a notification and publishes it on `notifications:webhook`.
   The event already carries the recipient's webhook settings, so the relay
   never queries the database.
2. The relay drops events whose recipient has webhooks disabled, or whose
   category the recipient turned off — the backend resolves both before
   publishing.
3. The event is POSTed as JSON to the recipient's URL, with retries on
   transport errors, `429` and `5xx`.

## Webhook request

```
POST <the user's webhook URL>
Content-Type: application/json
User-Agent: git-web-review-http-relay/1.0
X-Git-Web-Review-Event: REVIEW_STATUS_CHANGED
X-Git-Web-Review-Notification-Id: 0f0b...
X-Git-Web-Review-Delivery: 9a0c...
X-Git-Web-Review-Attempt: 1
```

```json
{
  "app": "git-web-review",
  "event": "REVIEW_STATUS_CHANGED",
  "notificationId": "0f0b...",
  "deliveryId": "9a0c...",
  "createdAt": "2026-09-11T09:12:44.512Z",
  "sentAt": "2026-09-11T09:12:44.902Z",
  "url": "https://git-web-review.company.tld/review/rev-42",
  "user": {
    "id": "7d1e...",
    "email": "dev@example.test",
    "nickname": "dev",
    "locale": "FR"
  },
  "payload": {
    "reviewId": "rev-42",
    "title": "Refonte du parser de commits",
    "previousStatus": "PENDING",
    "nextStatus": "IN_REVIEW",
    "actorNickname": "marie",
    "sourceProject": "kernel",
    "sourceBranch": "topic/parser",
    "gitwebUrl": "https://git.company.tld/?p=kernel"
  }
}
```

`payload` is the backend's notification payload forwarded **untouched**, key
for key. Its shape depends on `event`, and a field added backend-side reaches
endpoints without a relay release. The relay never inspects it.

Everything else is envelope:

| Field            | Meaning                                                                 |
| ---------------- | ----------------------------------------------------------------------- |
| `event`          | Notification type. Unknown types are relayed like any other.            |
| `notificationId` | The backend notification row; stable, and the same for every recipient. |
| `deliveryId`     | This delivery; stable across its retries, so receivers can deduplicate. |
| `createdAt`      | When the backend wrote the notification.                                |
| `sentAt`         | When the relay built this request.                                      |
| `url`            | Review link. The one derived field: the payload carries only a review id, and building the link needs `FRONTEND_URL`, which only the relay has. Falls back to `gitwebUrl`, and is `""` when the event links to neither. |
| `user`           | Who the notification is for. Their other transports' settings are never included. |

A delivery counts as successful on any `2xx`. `429` and `5xx` are retried with
exponential backoff and jitter, honouring `Retry-After`; other `4xx` are
treated as permanent and dropped. Redirects are not followed — the
notification goes to the configured URL or nowhere.

## Endpoints

The relay also serves a small HTTP surface of its own:

| Method | Path      | Description                                              |
| ------ | --------- | -------------------------------------------------------- |
| `GET`  | `/health` | `200` when the Redis subscription is up, `503` otherwise. |
| `GET`  | `/status` | Configuration summary and delivery counters.              |

## Configuration

| Variable                         | Default                          | Description                                                             |
| -------------------------------- | -------------------------------- | ----------------------------------------------------------------------- |
| `PORT`                           | `3002`                           | Port for `/health` and `/status`.                                       |
| `REDIS_URL`                      | `redis://localhost:6379`         | Redis connection string.                                                |
| `REDIS_CHANNEL`                  | `notifications:webhook`          | Channel the backend publishes on.                                       |
| `FRONTEND_URL`                   | `http://localhost:5173`          | Base URL used to build the `url` field.                                 |
| `HTTP_RELAY_DRY_RUN`             | `false`                          | Log the request instead of sending it.                                  |
| `WEBHOOK_WORKERS`                | `4`                              | Concurrent deliveries.                                                  |
| `WEBHOOK_QUEUE_SIZE`             | `1024`                           | Pending deliveries held in memory; events are dropped when it is full.  |
| `WEBHOOK_TIMEOUT`                | `10s`                            | Per-request timeout.                                                    |
| `WEBHOOK_MAX_ATTEMPTS`           | `3`                              | Total attempts, retries included.                                       |
| `WEBHOOK_RETRY_BASE_DELAY`       | `1s`                             | First backoff delay; it doubles per attempt.                            |
| `WEBHOOK_RETRY_MAX_DELAY`        | `30s`                            | Backoff ceiling.                                                        |
| `WEBHOOK_USER_AGENT`             | `git-web-review-http-relay/1.0`  | `User-Agent` sent with each delivery.                                   |
| `WEBHOOK_ALLOWED_HOSTS`          | empty (all)                      | Comma-separated hosts users may target; subdomains of a listed host pass. Empty, or `*`, allows every host. |
| `WEBHOOK_BLOCK_PRIVATE_NETWORKS` | `false`                          | Refuse targets resolving to loopback, private or link-local addresses.  |

Durations accept a Go duration (`10s`, `1m500ms`) or a bare number of
milliseconds.

`WEBHOOK_ALLOWED_HOSTS` follows the same spelling as the other allow-lists in
the stack: leave it empty or set it to `*` to allow every host, or list the
hosts users may target.

```env
WEBHOOK_ALLOWED_HOSTS=*
WEBHOOK_ALLOWED_HOSTS=chat.company.tld,hooks.company.tld
WEBHOOK_ALLOWED_HOSTS=*.company.tld
```

A listed host also matches its subdomains, so `company.tld` and `*.company.tld`
are equivalent; the `*.` prefix is accepted for readability.

`WEBHOOK_ALLOWED_HOSTS` and `WEBHOOK_BLOCK_PRIVATE_NETWORKS` are both
unrestricted by default, because the usual target is an internal chat server.
Tighten them when the users setting these URLs are not fully trusted: the
webhook URL is user-supplied, so by default the relay will call whatever it is
given from inside the network.

## Development

```sh
go test ./...
go run .
```

```sh
docker compose up --build http-relay
```
