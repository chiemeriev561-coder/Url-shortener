# URL Shortener

A self-contained Go URL shortener with persistent storage, expiry support, click analytics, management APIs, and a built-in web UI.

## Features

This project now includes all ten upgrades:

1. URL validation and normalization
2. Persistent storage in a local JSON file
3. Custom short codes
4. Expiry dates for links
5. Click analytics
6. Management REST endpoints to list, inspect, and delete links
7. Configurable base URL with `BASE_URL`
8. Stronger random code generation with `crypto/rand`
9. Automated tests
10. Simple frontend page for creating and managing links

## Run

```bash
go run shortner.go
```

Default server settings:

- `BIND_ADDR=:8080`
- `BASE_URL=http://localhost:8080`
- `DATA_FILE=data/links.json`

Example with custom configuration:

```bash
BASE_URL=https://sho.rt \
BIND_ADDR=:8080 \
DATA_FILE=storage/links.json \
go run shortner.go
```

## Storage

Links are persisted to a JSON file on disk.

- The app creates the data directory automatically if it does not exist.
- Data survives process restarts.
- Each link stores its metadata, including analytics and optional expiry.

## API

## Create a short URL

`POST /shorten`

You can also use `POST /links`. Both endpoints create links.

Request body:

```json
{
  "url": "https://example.com/docs",
  "custom_code": "docs",
  "expires_at": "2026-12-31T23:59:59Z"
}
```

Fields:

- `url` is required and must be a valid `http` or `https` URL
- `custom_code` is optional
- `expires_at` is optional and must be a future RFC3339 timestamp

Response:

```json
{
  "code": "docs",
  "short_url": "http://localhost:8080/docs",
  "original_url": "https://example.com/docs",
  "created_at": "2026-05-25T01:10:00Z",
  "expires_at": "2026-12-31T23:59:59Z",
  "expired": false,
  "clicks": 0
}
```

Example:

```bash
curl -X POST http://localhost:8080/shorten \
  -H "Content-Type: application/json" \
  -d '{
    "url":"https://example.com/docs",
    "custom_code":"docs",
    "expires_at":"2026-12-31T23:59:59Z"
  }'
```

## Redirect to the original URL

`GET /{code}`

Example:

```bash
curl -i http://localhost:8080/docs
```

Behavior:

- Active links return `303 See Other`
- Missing links return `404 Not Found`
- Expired links return `410 Gone`

Each successful redirect updates analytics:

- `clicks`
- `last_accessed_at`
- `last_referrer`
- `last_user_agent`

## List all links

`GET /links`

Response:

```json
[
  {
    "code": "docs",
    "short_url": "http://localhost:8080/docs",
    "original_url": "https://example.com/docs",
    "created_at": "2026-05-25T01:10:00Z",
    "expires_at": "2026-12-31T23:59:59Z",
    "expired": false,
    "clicks": 4,
    "last_accessed_at": "2026-05-25T01:40:00Z",
    "last_referrer": "https://dashboard.example",
    "last_user_agent": "Mozilla/5.0"
  }
]
```

## Get one link

`GET /links/{code}`

Example:

```bash
curl http://localhost:8080/links/docs
```

This returns the stored metadata for that short code, including analytics and expiry status.

## Delete one link

`DELETE /links/{code}`

Example:

```bash
curl -X DELETE http://localhost:8080/links/docs
```

Response:

- `204 No Content` on success
- `404 Not Found` if the code does not exist

## Web UI

Open:

```text
http://localhost:8080/
```

The page lets you:

- create links
- set a custom code
- choose an expiry time
- see all saved links
- view click counts and last access times
- delete links

## Validation Rules

### URL validation and normalization

- Only `http://` and `https://` URLs are accepted
- Scheme and host are normalized to lowercase
- Equivalent URLs reuse the same generated code when no custom alias is supplied

### Custom code rules

- Allowed characters: letters, numbers, hyphens, underscores
- Reserved values such as `shorten` and `links` are rejected
- If a custom code is already assigned to a different URL, the API returns `409 Conflict`

### Expiry rules

- `expires_at` must be in the future
- Expired links remain visible in the management API
- Expired links stop redirecting and return `410 Gone`

## Response Codes

The service returns:

- `200 OK` for successful `GET /links` and `GET /links/{code}`
- `201 Created` for successful link creation
- `204 No Content` for successful deletion
- `303 See Other` for redirects
- `400 Bad Request` for invalid JSON, invalid URLs, invalid custom codes, or invalid expiry values
- `404 Not Found` for unknown links
- `405 Method Not Allowed` for unsupported methods
- `409 Conflict` for custom code collisions
- `410 Gone` for expired links
- `500 Internal Server Error` for unexpected failures

## Test

```bash
go test ./...
```

Current automated coverage includes:

- URL normalization
- invalid URL rejection
- custom code behavior
- persistence across reloads
- expiry handling
- click analytics updates
- management API behavior
- redirect behavior
- environment-based configuration

## Project Notes

- The project is intentionally dependency-light and uses only the Go standard library.
- Persistence is implemented with a JSON file rather than an external database.
- This makes local development simple while still solving the “data disappears on restart” problem.
