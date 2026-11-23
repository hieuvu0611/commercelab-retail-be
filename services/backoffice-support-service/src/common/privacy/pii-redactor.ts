export const REDACTED_VALUE = '[REDACTED]';
export const REDACTED_ADDRESS = '[REDACTED_ADDRESS]';

const EMAIL_PATTERN = /[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g;
const INTL_PHONE_PATTERN =
  /(?<![A-Za-z0-9_-])(\+[1-9]\d{0,2}[\s.-]?(?:\(\d{1,4}\)|\d{1,4})(?:[\s.-]?\d{2,4}){2,4})(?![A-Za-z0-9_-])/g;
const VN_PHONE_PATTERN =
  /(?<![A-Za-z0-9_-])(0(?:3|5|7|8|9)\d(?:[\s.-]?\d{3,4}){2})(?![A-Za-z0-9_-])/g;
const BEARER_TOKEN_PATTERN = /\bBearer\s+[A-Za-z0-9._~+/=-]{8,}/gi;

const JSON_OBJECT_ADDRESS_PATTERN =
  /"(shippingAddress|shipping_address|billingAddress|billing_address|deliveryAddress|delivery_address|address)"\s*:\s*\{[^{}]*\}/gi;

const SENSITIVE_FIELD_NAMES =
  'address|shippingAddress|shipping_address|billingAddress|billing_address|deliveryAddress|delivery_address|' +
  'line1|line2|street|ward|district|province|recipient|recipientName|recipient_name|fullName|full_name|' +
  'phone|phoneNumber|phone_number|' +
  'password|currentPassword|current_password|newPassword|new_password|' +
  'token|accessToken|access_token|refreshToken|refresh_token|authorization|secret';

const JSON_STRING_SENSITIVE_PATTERN = new RegExp(
  `"(?:${SENSITIVE_FIELD_NAMES})"\\s*:\\s*"(?:[^"\\\\]|\\\\.)*"`,
  'gi',
);

const KV_SENSITIVE_PATTERN = new RegExp(
  `\\b(${SENSITIVE_FIELD_NAMES})=("[^"]*"|'[^']*'|Bearer\\s+[^\\s,&;]+|[^\\s,&;]+)`,
  'gi',
);

const ADDRESS_EXACT_KEYS = new Set([
  'line1',
  'line2',
  'street',
  'ward',
  'district',
  'province',
]);

const RECIPIENT_EXACT_KEYS = new Set([
  'recipient',
  'recipientname',
  'recipient_name',
  'fullname',
  'full_name',
]);

function isEmailKey(normalizedKey: string): boolean {
  return normalizedKey.includes('email');
}

function isPhoneKey(normalizedKey: string): boolean {
  return normalizedKey.includes('phone');
}

function isAddressKey(normalizedKey: string): boolean {
  return normalizedKey.includes('address') || ADDRESS_EXACT_KEYS.has(normalizedKey);
}

function isSecretOrRecipientKey(normalizedKey: string): boolean {
  return (
    RECIPIENT_EXACT_KEYS.has(normalizedKey) ||
    normalizedKey.includes('password') ||
    normalizedKey.includes('token') ||
    normalizedKey.includes('secret') ||
    normalizedKey === 'authorization'
  );
}

function digitsOnly(value: string): string {
  return value.replace(/\D/g, '');
}

export function redactEmail(email: string): string {
  const trimmed = email.trim().toLowerCase();
  if (!trimmed) return '';
  const at = trimmed.lastIndexOf('@');
  if (at < 1 || at === trimmed.length - 1) return '***';
  const local = trimmed.slice(0, at);
  const domain = trimmed.slice(at + 1);
  if (local.length <= 1 || /^\*+$/.test(local)) {
    return `***@${domain}`;
  }
  return `${local[0]}***@${domain}`;
}

export function redactPhone(phone: string): string {
  const trimmed = phone.trim();
  if (!trimmed) return '';
  if (trimmed.length >= 7) {
    return `${trimmed.slice(0, 3)}****${trimmed.slice(-3)}`;
  }
  return '***';
}

export function redactAddress(address: string): string {
  if (!address.trim()) return '';
  return REDACTED_ADDRESS;
}

function redactKeyValue(key: string, rawValue: string): string {
  const normalizedKey = key.toLowerCase();
  if (isPhoneKey(normalizedKey)) {
    return redactPhone(rawValue);
  }
  if (isAddressKey(normalizedKey)) {
    return redactAddress(rawValue);
  }
  return rawValue.trim() ? REDACTED_VALUE : '';
}

export function redactText(text: string): string {
  if (!text) return '';

  let out = text.replace(JSON_OBJECT_ADDRESS_PATTERN, `"$1":"${REDACTED_ADDRESS}"`);

  out = out.replace(JSON_STRING_SENSITIVE_PATTERN, (match) => {
    const colonIndex = match.indexOf(':');
    const keyPart = match.slice(0, colonIndex);
    const key = keyPart.trim().replace(/^"|"$/g, '');
    const rawValue = match.slice(colonIndex + 1).trim().replace(/^"|"$/g, '');
    return `${keyPart}:"${redactKeyValue(key, rawValue)}"`;
  });

  out = out.replace(KV_SENSITIVE_PATTERN, (_match, key: string, valuePart: string) => {
    const rawValue = valuePart.replace(/^["']|["']$/g, '');
    return `${key}=${redactKeyValue(key, rawValue)}`;
  });

  out = out.replace(BEARER_TOKEN_PATTERN, `Bearer ${REDACTED_VALUE}`);

  out = out.replace(EMAIL_PATTERN, (match) => redactEmail(match));

  out = out.replace(INTL_PHONE_PATTERN, (match) => {
    const digits = digitsOnly(match);
    if (digits.length < 9 || digits.length > 15) return match;
    return redactPhone(`+${digits}`);
  });

  out = out.replace(VN_PHONE_PATTERN, (match) => {
    const digits = digitsOnly(match);
    if (digits.length < 9 || digits.length > 11) return match;
    return redactPhone(digits);
  });

  return out;
}

function redactValue(value: unknown, keyHint?: string): unknown {
  if (typeof value === 'string') {
    if (keyHint) {
      const normalizedKey = keyHint.toLowerCase();
      if (isEmailKey(normalizedKey)) return redactEmail(value);
      if (isPhoneKey(normalizedKey)) return redactPhone(value);
      if (isAddressKey(normalizedKey)) return redactAddress(value);
      if (isSecretOrRecipientKey(normalizedKey)) {
        return value.trim() ? REDACTED_VALUE : '';
      }
    }
    return redactText(value);
  }

  if (Array.isArray(value)) {
    return value.map((item) => redactValue(item, keyHint));
  }

  if (value instanceof Date) {
    return value;
  }

  if (value !== null && typeof value === 'object') {
    const masked: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      masked[k] = redactValue(v, k);
    }
    return masked;
  }

  return value;
}

export function redactObject(
  input?: Record<string, unknown>,
): Record<string, unknown> | undefined {
  if (!input) return undefined;
  return redactValue(input) as Record<string, unknown>;
}
