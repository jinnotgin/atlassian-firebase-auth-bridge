# Atlassian Firebase Auth Bridge

OAuth bridge that lets an app use Atlassian login as the primary login method while still using Firebase Auth and Firestore.

The frontend does **not** talk to Atlassian directly and does **not** store Atlassian secrets. The bridge handles the Atlassian OAuth flow, creates or updates the Firebase Auth user, mints a Firebase custom token, and returns that token to the frontend through a short-lived one-time login code.

---

## Flow

1. App redirects to:

    ```text
    /auth/atlassian/start
    ```

Optionally with a safe relative redirect path:

    ```text
    /auth/atlassian/start?redirect=/overview
    ```

2. User signs into Atlassian.

3. Atlassian redirects back to:

   ```text
   /auth/atlassian/callback
   ```

4. Backend exchanges the Atlassian authorization code for an Atlassian access token.

5. Backend calls Atlassian:

   ```text
   https://api.atlassian.com/me
   ```

6. Backend requires Atlassian to return:

   ```text
   account_id
   email
   ```

7. Backend creates or updates a Firebase Auth user using UID:

   ```text
   atlassian:{account_id}
   ```

8. Backend mints a Firebase custom token.

9. Backend stores the Firebase custom token in Firestore under a short-lived one-time login code.

10. Backend redirects the frontend to:

    ```text
    /auth/callback?login_code=...&redirect=/overview
    ```

11. App exchanges the `login_code` with the bridge.

12. App calls:

    ```ts
    signInWithCustomToken(auth, firebaseCustomToken)
    ```

13. App redirects the user to the returned redirect path, or `/overview`.

---

## API contract

### Start Atlassian login

```http
GET /auth/atlassian/start
```

Optional query parameters:

```text
redirect=/some/frontend/path
frontend_origin=http://localhost:5173
```

Example:

```text
https://AUTH_BRIDGE_BASE_URL/auth/atlassian/start?redirect=/overview&frontend_origin=http://localhost:5173
```

The `frontend_origin` must be an origin (scheme + host, no path) listed in `ALLOWED_FRONTEND_ORIGINS`. If missing or not allowlisted, it falls back to `FRONTEND_BASE_URL`.

The `redirect` value must be a relative frontend path. Invalid values fall back to:

```text
/overview
```

Valid examples:

```text
/overview
/projects/123
/settings
```

Invalid examples:

```text
https://example.com
//example.com
javascript:alert(1)
```

---

### Atlassian callback

```http
GET /auth/atlassian/callback
```

This is called by Atlassian, not by the frontend directly.

The callback URL must exactly match the value configured in the Atlassian developer console and in `ATLASSIAN_REDIRECT_URI`.

---

### Exchange login code

```http
POST /auth/session/exchange
Content-Type: application/json
```

Request body:

```json
{
  "loginCode": "opaque-login-code-from-query-string"
}
```

Successful response:

```json
{
  "firebaseCustomToken": "firebase-custom-token",
  "uid": "atlassian:712020:abc123",
  "email": "user@example.com",
  "redirect": "/overview"
}
```

Error response:

```json
{
  "error": "invalid_or_expired_login_code"
}
```

The login code is one-time-use and short-lived. After a successful exchange, it is deleted from Firestore.

---

## Required environment

```env
ATLASSIAN_CLIENT_ID=
ATLASSIAN_CLIENT_SECRET=
ATLASSIAN_REDIRECT_URI=
FRONTEND_BASE_URL=
ALLOWED_FRONTEND_ORIGINS=
ALLOWED_CORS_ORIGINS=
LOGIN_CODE_SECRET=
GOOGLE_APPLICATION_CREDENTIALS=/secrets/firebase-service-account.json
```

Example:

```env
ATLASSIAN_CLIENT_ID=your-atlassian-client-id
ATLASSIAN_CLIENT_SECRET=your-atlassian-client-secret
ATLASSIAN_REDIRECT_URI=https://your-auth-bridge.example.com/auth/atlassian/callback
FRONTEND_BASE_URL=https://your-frontend.example.com
ALLOWED_FRONTEND_ORIGINS=https://your-frontend.example.com,http://localhost:5173,http://localhost:3000
ALLOWED_CORS_ORIGINS=https://your-frontend.example.com,http://localhost:5173,http://localhost:3000
LOGIN_CODE_SECRET=your-long-random-secret
GOOGLE_APPLICATION_CREDENTIALS=/secrets/firebase-service-account.json
```

### Environment variable notes

#### `ATLASSIAN_CLIENT_ID`

Atlassian OAuth client ID.

#### `ATLASSIAN_CLIENT_SECRET`

Atlassian OAuth client secret.

Keep this only on the backend. Do not expose it to the frontend.

#### `ATLASSIAN_REDIRECT_URI`

The callback URL registered with Atlassian.

Example:

```text
https://your-auth-bridge.example.com/auth/atlassian/callback
```

This must match the callback URL in the Atlassian developer console.

#### `FRONTEND_BASE_URL`

The deployed frontend base URL.

Example:

```text
https://your-frontend.example.com
```

The backend redirects users here after Atlassian login. Also used as the default `frontend_origin` when the query parameter is missing or not allowlisted.

#### `ALLOWED_FRONTEND_ORIGINS`

Comma-separated list of frontend origins the backend is allowed to redirect to after OAuth.

The frontend can pass `frontend_origin` as a query parameter to `/auth/atlassian/start`. The value must match one of these origins exactly. If missing or not allowlisted, `FRONTEND_BASE_URL` is used.

Example:

```env
ALLOWED_FRONTEND_ORIGINS=https://your-frontend.example.com,http://localhost:5173,http://localhost:3000
```

For production-only deployments:

```env
ALLOWED_FRONTEND_ORIGINS=https://your-frontend.example.com
```

#### `ALLOWED_CORS_ORIGINS`

Comma-separated list of frontend origins allowed to call:

```text
POST /auth/session/exchange
```

Example:

```env
ALLOWED_CORS_ORIGINS=https://your-frontend.example.com,http://localhost:5173,http://localhost:3000
```

For production-only deployments:

```env
ALLOWED_CORS_ORIGINS=https://your-frontend.example.com
```

#### `LOGIN_CODE_SECRET`

Secret used to sign OAuth state.

Generate it with:

```bash
openssl rand -base64 48
```

#### `GOOGLE_APPLICATION_CREDENTIALS`

Path to the Firebase service account JSON file.

Example:

```env
GOOGLE_APPLICATION_CREDENTIALS=/secrets/firebase-service-account.json
```

---

## Firestore

This bridge uses Firestore to store short-lived one-time login codes.

Collection:

```text
authLoginCodes
```

Documents are created automatically by the backend.

Each document stores:

```json
{
  "firebaseCustomToken": "...",
  "uid": "atlassian:...",
  "email": "user@example.com",
  "atlassianAccountId": "...",
  "redirect": "/overview",
  "expiresAt": "...",
  "createdAt": "..."
}
```

The frontend should never read or write this collection directly.

---

## Firestore rules

Deny all client access to login codes:

```js
rules_version = '2';

service cloud.firestore {
  match /databases/{database}/documents {
    match /authLoginCodes/{loginCode} {
      allow read, write: if false;
    }

    // App-specific rules go below.
    match /users/{userId} {
      allow read, write: if request.auth != null
        && request.auth.uid == userId;
    }
  }
}
```

The backend uses the Firebase Admin SDK, so it can still access Firestore even when client rules deny access.

---

## Recommended Firestore TTL

Enable Firestore TTL for abandoned login codes.

Collection group:

```text
authLoginCodes
```

TTL field:

```text
expiresAt
```

The backend already deletes login codes after successful exchange. TTL is only for cleanup when users abandon the login flow.

---

## Firebase Auth behavior

Firebase users are identified by UID:

```text
atlassian:{account_id}
```

Example:

```text
atlassian:712020:abc123
```

The backend should create or update the Firebase Auth user with:

```text
email
displayName
photoURL
```

The custom token includes claims such as:

```json
{
  "provider": "atlassian",
  "atlassianAccountId": "712020:abc123",
  "email": "user@example.com"
}
```

---

## Frontend integration

### Required frontend environment

```env
VITE_AUTH_BACKEND_BASE_URL=https://your-auth-bridge.example.com
```

### Login button

```ts
function loginWithAtlassian() {
  const redirect = encodeURIComponent(
    router.currentRoute.value.fullPath || "/overview"
  );
  const frontendOrigin = encodeURIComponent(window.location.origin);

  window.location.href =
    `${import.meta.env.VITE_AUTH_BACKEND_BASE_URL}/auth/atlassian/start?redirect=${redirect}&frontend_origin=${frontendOrigin}`;
}
```

If deep-link preservation is not needed:

```ts
function loginWithAtlassian() {
  const frontendOrigin = encodeURIComponent(window.location.origin);

  window.location.href =
    `${import.meta.env.VITE_AUTH_BACKEND_BASE_URL}/auth/atlassian/start?redirect=/overview&frontend_origin=${frontendOrigin}`;
}
```

### Callback route

Add a frontend route such as:

```ts
{
  path: "/auth/callback",
  component: () => import("@/views/AuthCallbackView.vue"),
}
```

### Callback page behavior

The callback page should:

1. Read `login_code` from the query string.
2. POST it to `/auth/session/exchange`.
3. Receive `firebaseCustomToken`.
4. Call `signInWithCustomToken()`.
5. Redirect the user to the returned redirect path.
6. Use `router.replace()`, not `router.push()`, so the callback URL is removed from browser history.

Example:

```ts
import { signInWithCustomToken } from "firebase/auth";

function safeRedirectPath(value: unknown): string {
  if (typeof value !== "string") return "/overview";
  if (!value.startsWith("/")) return "/overview";
  if (value.startsWith("//")) return "/overview";
  return value;
}

async function completeAtlassianLogin(route, router, auth) {
  const loginCode = route.query.login_code;

  if (typeof loginCode !== "string" || !loginCode) {
    router.replace("/login?error=missing_login_code");
    return;
  }

  const res = await fetch(
    `${import.meta.env.VITE_AUTH_BACKEND_BASE_URL}/auth/session/exchange`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        loginCode,
      }),
    }
  );

  if (!res.ok) {
    router.replace("/login?error=invalid_or_expired_login_code");
    return;
  }

  const data = await res.json();

  await signInWithCustomToken(auth, data.firebaseCustomToken);

  router.replace(safeRedirectPath(data.redirect));
}
```

---

## Local run

Create a local `.env` file:

```env
PORT=8080
ATLASSIAN_CLIENT_ID=your-atlassian-client-id
ATLASSIAN_CLIENT_SECRET=your-atlassian-client-secret
ATLASSIAN_REDIRECT_URI=http://localhost:8080/auth/atlassian/callback
FRONTEND_BASE_URL=http://localhost:5173
ALLOWED_CORS_ORIGINS=http://localhost:5173,http://localhost:3000
LOGIN_CODE_SECRET=your-long-random-secret
GOOGLE_APPLICATION_CREDENTIALS=/path/to/firebase-service-account.json
```

Run:

```bash
go run ./cmd/server
```

---

## Docker

Build:

```bash
docker build -t atlassian-firebase-auth-bridge .
```

Run:

```bash
docker run --rm -p 8080:8080 --env-file .env \
  -v "$PWD/firebase-service-account.json:/secrets/firebase-service-account.json:ro" \
  atlassian-firebase-auth-bridge
```

---

## Cloud Run deployment notes

The service should be deployed with:

```text
ATLASSIAN_CLIENT_ID
ATLASSIAN_CLIENT_SECRET
ATLASSIAN_REDIRECT_URI
FRONTEND_BASE_URL
ALLOWED_CORS_ORIGINS
LOGIN_CODE_SECRET
GOOGLE_APPLICATION_CREDENTIALS
```

Recommended runtime permissions:

```text
roles/datastore.user
roles/secretmanager.secretAccessor
```

`roles/secretmanager.secretAccessor` is only needed if secrets are mounted from Secret Manager.

Firestore means the service no longer needs to rely on process memory for login codes. This makes the bridge safe across:

```text
Cloud Run restarts
scale-to-zero
multiple instances
new deployments
revision rollouts
```

---

## Security notes

* Do not put Atlassian secrets in the frontend.
* Do not put Firebase service account credentials in the frontend.
* `login_code` is opaque and one-time-use.
* `login_code` expires quickly.
* The Firebase custom token is not placed directly in the browser URL.
* The frontend should remove the callback URL from browser history using `router.replace()`.
* Only allow known frontend origins in `ALLOWED_CORS_ORIGINS`.
* Only allow relative redirect paths.
* Deny direct client access to `authLoginCodes` in Firestore rules.

---

## Health check

```http
GET /healthz
```

Response:

```json
{
  "ok": "true"
}
```

