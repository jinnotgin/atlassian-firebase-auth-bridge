# Atlassian Firebase Auth Bridge

OAuth bridge that lets an app use Atlassian login as the primary login method while still using Firebase Auth and Firestore.

## Flow

1. App redirects to `/auth/atlassian/start`
2. User signs into Atlassian
3. Atlassian redirects to `/auth/atlassian/callback`
4. Backend calls Atlassian `/me`
5. Backend requires an email address
6. Backend mints Firebase custom token using UID `atlassian:{account_id}`
7. App calls `signInWithCustomToken()`

## Required environment

```env
ATLASSIAN_CLIENT_ID=
ATLASSIAN_CLIENT_SECRET=
ATLASSIAN_REDIRECT_URI=
FRONTEND_BASE_URL=
LOGIN_CODE_SECRET=
GOOGLE_APPLICATION_CREDENTIALS=/secrets/firebase-service-account.json
```

### Generate LOGIN_CODE_SECRET with:
```
openssl rand -base64 48
```

## Local Run
```
go run ./cmd/server
```

## Docker
```
docker build -t atlassian-firebase-auth-bridge .
docker run --rm -p 8080:8080 --env-file .env \
  -v "$PWD/firebase-service-account.json:/secrets/firebase-service-account.json:ro" \
  atlassian-firebase-auth-bridge
```
