# Atlassian Firebase Auth Bridge

A small Go service that lets a frontend application use Atlassian OAuth as the sign-in provider while still using Firebase Authentication as the application session system.

## Problem Statement

Firebase Auth does not provide a built-in Atlassian sign-in provider. A frontend-only Atlassian OAuth integration would also expose client secrets and make it awkward to mint Firebase custom tokens safely.

This service sits between the frontend application, Atlassian, and Firebase:

- The frontend application redirects users to the auth bridge service to start Atlassian sign-in.
- The auth bridge service owns the Atlassian OAuth client secret.
- The auth bridge service validates the Atlassian user, creates or updates the matching Firebase Auth user, and mints a Firebase custom token.
- Firestore stores the short-lived one-time login code used to pass the result back to the frontend safely.
- The frontend application exchanges that login code for the Firebase custom token and calls `signInWithCustomToken()`.

The Firebase custom token is never placed directly in a browser redirect URL.

## Terminology

This README uses these names consistently:

- **Frontend application**: your web UI, for example a Vue, React, or other browser app using the Firebase client SDK.
- **Auth bridge service**: this Go service.
- **Atlassian OAuth app**: the OAuth 2.0 integration configured in the Atlassian developer console.
- **Firebase project**: the Firebase project containing Firebase Auth and Firestore.
- **Login code**: this service's short-lived one-time code stored in Firestore. It is not the Atlassian authorization code.

## Requirements

You need all of the following before the bridge can complete a login:

- An Atlassian OAuth app with the `read:me` scope.
- An Atlassian callback URL that points to this service:

  ```text
  https://your-auth-bridge.example.com/auth/atlassian/callback
  ```

- A Firebase project with Firebase Authentication enabled.
- Firestore enabled in the same Firebase project.
- A Firebase service account JSON file available to the auth bridge service.
- Permission for the service account to use Firebase Auth and read/write Firestore.
- A frontend route that handles:

  ```text
  /auth/callback
  ```

- Frontend code that exchanges the login code and calls Firebase Auth:

  ```ts
  signInWithCustomToken(auth, firebaseCustomToken)
  ```

### Why Firestore is required

After Atlassian redirects back to the auth bridge service, the service mints a Firebase custom token. Instead of putting that token in the browser URL, the service stores it in Firestore under an opaque one-time login code.

The frontend application receives only the login code:

```text
/auth/callback?login_code=...
```

It then exchanges the login code with:

```http
POST /auth/session/exchange
```

This Firestore-backed handoff makes the login flow work across multiple service instances, Cloud Run restarts, scale-to-zero, and new deployments.

## Architecture

```text
Frontend application
  | 1. redirect user to /auth/atlassian/start
  v
Auth bridge service
  | 2. redirect user to Atlassian OAuth
  v
Atlassian
  | 3. callback with authorization code
  v
Auth bridge service
  | 4. exchange Atlassian code
  | 5. fetch Atlassian profile
  | 6. create/update Firebase Auth user
  | 7. mint Firebase custom token
  | 8. store token in Firestore under login code
  v
Frontend application
  | 9. exchange login code
  | 10. sign in with Firebase custom token
  v
Firebase Auth session
```

## Login Flow

1. The frontend application redirects the browser to the auth bridge service:

   ```text
   /auth/atlassian/start
   ```

   It can include a safe relative redirect path and its own origin:

   ```text
   /auth/atlassian/start?redirect=/dashboard&frontend_origin=http://localhost:5173
   ```

2. The auth bridge service redirects the user to Atlassian.

3. The user signs in with Atlassian.

4. Atlassian redirects back to the auth bridge service:

   ```text
   /auth/atlassian/callback
   ```

5. The auth bridge service exchanges the Atlassian authorization code for an Atlassian access token.

6. The auth bridge service calls:

   ```text
   https://api.atlassian.com/me
   ```

7. Atlassian must return both:

   ```text
   account_id
   email
   ```

8. The auth bridge service creates or updates a Firebase Auth user with this UID:

   ```text
   atlassian:{account_id}
   ```

9. The auth bridge service mints a Firebase custom token.

10. The auth bridge service stores that custom token in Firestore under a short-lived one-time login code.

11. The auth bridge service redirects the browser back to the frontend application:

    ```text
    /auth/callback?login_code=...&redirect=/dashboard
    ```

12. The frontend application posts the `login_code` to the auth bridge service.

13. The auth bridge service consumes and deletes the login code, then returns the Firebase custom token.

14. The frontend application signs in to Firebase:

    ```ts
    signInWithCustomToken(auth, firebaseCustomToken)
    ```

15. The frontend application redirects the user to the returned redirect path, or `/`.

## Configuration

Required environment variables:

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

### Environment variable reference

| Variable | Purpose |
| --- | --- |
| `ATLASSIAN_CLIENT_ID` | Atlassian OAuth client ID. |
| `ATLASSIAN_CLIENT_SECRET` | Atlassian OAuth client secret. Keep this only on the auth bridge service. |
| `ATLASSIAN_REDIRECT_URI` | Exact callback URL registered in the Atlassian developer console. |
| `FRONTEND_BASE_URL` | Default frontend origin used for redirects when no allowed `frontend_origin` is supplied. |
| `ALLOWED_FRONTEND_ORIGINS` | Comma-separated list of frontend origins the service may redirect users back to after OAuth. |
| `ALLOWED_CORS_ORIGINS` | Comma-separated list of frontend origins allowed to call `POST /auth/session/exchange`. |
| `LOGIN_CODE_SECRET` | Secret used to sign OAuth state. Generate a long random value. |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to the Firebase service account JSON file. Defaults to `/secrets/firebase-service-account.json` if unset. |

Generate `LOGIN_CODE_SECRET` with:

```bash
openssl rand -base64 48
```

### Redirect and CORS origins

`ALLOWED_FRONTEND_ORIGINS` controls where the auth bridge service may redirect the browser after Atlassian completes OAuth.

`ALLOWED_CORS_ORIGINS` controls which browser origins may call:

```http
POST /auth/session/exchange
```

For production-only deployments, these are usually the same single frontend origin:

```env
ALLOWED_FRONTEND_ORIGINS=https://your-frontend.example.com
ALLOWED_CORS_ORIGINS=https://your-frontend.example.com
```

For local development, include your dev server origins:

```env
ALLOWED_FRONTEND_ORIGINS=https://your-frontend.example.com,http://localhost:5173,http://localhost:3000
ALLOWED_CORS_ORIGINS=https://your-frontend.example.com,http://localhost:5173,http://localhost:3000
```

## Firestore

The auth bridge service writes login handoff records to this collection:

```text
authLoginCodes
```

Documents are created automatically. Each document stores:

```json
{
  "firebaseCustomToken": "...",
  "uid": "atlassian:712020:abc123",
  "email": "user@example.com",
  "atlassianAccountId": "712020:abc123",
  "redirect": "/dashboard",
  "expiresAt": "...",
  "createdAt": "..."
}
```

The frontend application should never read or write this collection directly.

### Firestore rules

Deny all client access to login codes:

```js
rules_version = '2';

service cloud.firestore {
  match /databases/{database}/documents {
    // Auth bridge handoff records are server-only and 
    // must not be read or written by frontend clients.
    match /authLoginCodes/{loginCode} {
      allow read, write: if false;
    }

    // Other existing rules go below.
    match /users/{userId} {
      allow read, write: if request.auth != null
        && request.auth.uid == userId;
    }
  }
}
```

The auth bridge service uses the Firebase Admin SDK, so it can still access Firestore even when client rules deny access.

### Recommended TTL

Enable Firestore TTL for abandoned login codes:

```text
Collection group: authLoginCodes
TTL field: expiresAt
```

The service deletes login codes after successful exchange. TTL is only for cleanup when users abandon the login flow.

## Firebase Auth Behavior

Firebase users are identified by Atlassian account ID:

```text
atlassian:{account_id}
```

Example:

```text
atlassian:712020:abc123
```

The auth bridge service creates or updates the Firebase Auth user with:

```text
email
displayName
photoURL
```

The Firebase custom token includes claims like:

```json
{
  "provider": "atlassian",
  "atlassianAccountId": "712020:abc123",
  "email": "user@example.com"
}
```

## API Contract

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
https://AUTH_BRIDGE_BASE_URL/auth/atlassian/start?redirect=/dashboard&frontend_origin=http://localhost:5173
```

`frontend_origin` must be an origin, meaning scheme plus host with no path:

```text
http://localhost:5173
```

It must be listed in `ALLOWED_FRONTEND_ORIGINS`. If it is missing or not allowlisted, the service falls back to `FRONTEND_BASE_URL`.

`redirect` must be a relative frontend path. Invalid values fall back to:

```text
/
```

Valid redirect examples:

```text
/dashboard
/projects/123
/settings
```

Invalid redirect examples:

```text
https://example.com
//example.com
javascript:alert(1)
```

### Atlassian callback

```http
GET /auth/atlassian/callback
```

This endpoint is called by Atlassian, not by the frontend application directly.

The callback URL must exactly match both:

- The callback URL configured in the Atlassian developer console.
- `ATLASSIAN_REDIRECT_URI`.

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
  "redirect": "/dashboard"
}
```

Error response:

```json
{
  "error": "invalid_or_expired_login_code"
}
```

The login code is one-time-use and short-lived. After a successful exchange, it is deleted from Firestore inside a transaction.

### Health check

```http
GET /healthz
```

Response:

```json
{
  "ok": "true"
}
```

## Frontend Integration

The frontend application needs one piece of configuration: the public base URL of the auth bridge service.

```text
https://your-auth-bridge.example.com
```

How you provide that value depends on your frontend. For example, a Vite application might use `VITE_AUTH_BRIDGE_BASE_URL`, while another frontend might use a different environment variable, runtime config object, or server-rendered setting.

### Login button

Build a URL to `/auth/atlassian/start` and send the browser there. The `redirect` value is optional; use any relative path that makes sense for your frontend after login.

```ts
const authBridgeBaseUrl = import.meta.env.VITE_AUTH_BRIDGE_BASE_URL;

function loginWithAtlassian() {
  const redirect = encodeURIComponent("/dashboard");
  const frontendOrigin = encodeURIComponent(window.location.origin);

  window.location.href =
    `${authBridgeBaseUrl}/auth/atlassian/start` +
    `?redirect=${redirect}&frontend_origin=${frontendOrigin}`;
}
```

### Frontend callback route

Add a frontend route for:

```text
/auth/callback
```

The callback page should:

1. Read `login_code` from the query string.
2. POST it to `/auth/session/exchange`.
3. Receive `firebaseCustomToken`.
4. Call `signInWithCustomToken()`.
5. Redirect the user to the returned redirect path.
6. Use `router.replace()`, so the callback URL is removed from browser history.

Example:

```ts
import { signInWithCustomToken } from "firebase/auth";

const authBridgeBaseUrl = import.meta.env.VITE_AUTH_BRIDGE_BASE_URL;

function safeRedirectPath(value: unknown): string {
  if (typeof value !== "string") return "/";
  if (!value.startsWith("/")) return "/";
  if (value.startsWith("//")) return "/";
  return value;
}

async function completeAtlassianLogin(route, router, auth) {
  const loginCode = route.query.login_code;

  if (typeof loginCode !== "string" || !loginCode) {
    router.replace("/login?error=missing_login_code");
    return;
  }

  const res = await fetch(
    `${authBridgeBaseUrl}/auth/session/exchange`,
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

## Local Development

Create a local `.env` file:

```env
PORT=8080
ATLASSIAN_CLIENT_ID=your-atlassian-client-id
ATLASSIAN_CLIENT_SECRET=your-atlassian-client-secret
ATLASSIAN_REDIRECT_URI=http://localhost:8080/auth/atlassian/callback
FRONTEND_BASE_URL=http://localhost:5173
ALLOWED_FRONTEND_ORIGINS=http://localhost:5173,http://localhost:3000
ALLOWED_CORS_ORIGINS=http://localhost:5173,http://localhost:3000
LOGIN_CODE_SECRET=your-long-random-secret
GOOGLE_APPLICATION_CREDENTIALS=/path/to/firebase-service-account.json
```

Run:

```bash
go run ./cmd/server
```

## Docker

Prebuilt Linux Docker images are published for `linux/amd64` and `linux/arm64` at:

```text
https://github.com/jinnotgin/atlassian-firebase-auth-bridge/pkgs/container/atlassian-firebase-auth-bridge
```

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

## Cloud Run Deployment Notes

Configure the service with:

```text
ATLASSIAN_CLIENT_ID
ATLASSIAN_CLIENT_SECRET
ATLASSIAN_REDIRECT_URI
FRONTEND_BASE_URL
ALLOWED_FRONTEND_ORIGINS
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

Because login codes are stored in Firestore, the service does not rely on process memory for login handoff state. This makes the bridge suitable for:

```text
Cloud Run restarts
scale-to-zero
multiple instances
new deployments
revision rollouts
```

## Security Notes

- Keep `ATLASSIAN_CLIENT_SECRET`, `LOGIN_CODE_SECRET`, and Firebase service account credentials in a server-side secret store.
- Only include trusted frontend origins in `ALLOWED_FRONTEND_ORIGINS` and `ALLOWED_CORS_ORIGINS`.
- Keep direct client access to `authLoginCodes` denied in Firestore rules.
