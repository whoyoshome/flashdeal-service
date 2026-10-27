// HMAC-SHA256 JWT helper for k6. Mirrors internal/platform/auth claims:
// { alg: HS256 }, { sub, role, iss, iat, exp }.
import encoding from 'k6/encoding';
import crypto from 'k6/crypto';

const SECRET = __ENV.JWT_SECRET || 'flashdeal-local-dev-secret-change-me!!';
const ISSUER = __ENV.JWT_ISSUER || 'flashdeal';

function b64url(data) {
  return encoding.b64encode(data, 'rawurl');
}

/**
 * issueJWT mints a Bearer-ready token for subject with the given role.
 * @param {string} subject
 * @param {string} role user|admin
 * @returns {string} compact JWT
 */
export function issueJWT(subject, role) {
  const header = b64url(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const now = Math.floor(Date.now() / 1000);
  const payload = b64url(
    JSON.stringify({
      sub: subject,
      role: role,
      iss: ISSUER,
      iat: now,
      exp: now + 3600,
    }),
  );
  const sig = crypto.hmac('sha256', SECRET, `${header}.${payload}`, 'base64rawurl');
  return `${header}.${payload}.${sig}`;
}

/**
 * authHeaders returns Authorization (and optional Idempotency-Key) headers.
 */
export function authHeaders(subject, role, idempotencyKey) {
  const headers = {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${issueJWT(subject, role)}`,
  };
  if (idempotencyKey) {
    headers['Idempotency-Key'] = idempotencyKey;
  }
  return headers;
}
