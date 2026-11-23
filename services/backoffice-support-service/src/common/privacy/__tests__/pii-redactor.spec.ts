import {
  REDACTED_ADDRESS,
  REDACTED_VALUE,
  redactAddress,
  redactEmail,
  redactObject,
  redactPhone,
  redactText,
} from '../pii-redactor';

describe('PII Redactor', () => {
  describe('redactEmail', () => {
    it('masks multi-character local parts while keeping the first character and domain', () => {
      expect(redactEmail('lan.nguyen@example.com')).toBe('l***@example.com');
      expect(redactEmail('Lan.Nguyen@Example.COM')).toBe('l***@example.com');
    });

    it('masks single-character local parts completely', () => {
      expect(redactEmail('a@example.com')).toBe('***@example.com');
    });

    it('handles empty and malformed emails', () => {
      expect(redactEmail('')).toBe('');
      expect(redactEmail('   ')).toBe('');
      expect(redactEmail('invalid-email')).toBe('***');
    });
  });

  describe('redactPhone', () => {
    it('preserves first 3 and last 3 characters for phone numbers with at least 7 characters', () => {
      expect(redactPhone('0901234567')).toBe('090****567');
      expect(redactPhone('+84901234567')).toBe('+84****567');
    });

    it('masks shorter non-empty values and preserves empty values', () => {
      expect(redactPhone('12345')).toBe('***');
      expect(redactPhone('')).toBe('');
      expect(redactPhone('   ')).toBe('');
    });
  });

  describe('redactAddress', () => {
    it('replaces non-empty addresses with REDACTED_ADDRESS and preserves empty strings', () => {
      expect(redactAddress('123 Le Loi, District 1, HCMC')).toBe(REDACTED_ADDRESS);
      expect(redactAddress('')).toBe('');
      expect(redactAddress('   ')).toBe('');
    });
  });

  describe('redactText', () => {
    it('redacts emails and VN/international phone numbers in free-text logs and paths', () => {
      const logLine =
        'http method=GET path=/api/v1/users/lan.nguyen@example.com/phone/0901234567/alt/+84912345678 status=200 durationMs=12.34 correlationId=req-1234567890abcdef';
      const redacted = redactText(logLine);

      expect(redacted).toContain('l***@example.com');
      expect(redacted).toContain('090****567');
      expect(redacted).toContain('+84****678');
      expect(redacted).not.toContain('lan.nguyen@example.com');
      expect(redacted).not.toContain('0901234567');
      expect(redacted).not.toContain('+84912345678');
      expect(redacted).toContain('durationMs=12.34');
      expect(redacted).toContain('correlationId=req-1234567890abcdef');
    });

    it('redacts JSON and key-value address, recipient, and secret fields', () => {
      const jsonPayload = JSON.stringify({
        orderId: 'O-20261123-0001',
        shippingAddress: { line1: '123 Le Loi', district: 'District 1' },
        address: '456 Nguyen Hue',
        street: 'Nguyen Hue',
        ward: 'Ben Nghe',
        province: 'HCMC',
        recipient: 'Lan Nguyen',
        fullName: 'Nguyen Thi Lan',
        password: 'SuperSecretPassword1!',
        currentPassword: 'OldPassword1!',
        newPassword: 'NewPassword2!',
        token: 'jwt-token-value-12345',
        authorization: 'Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig',
        secret: 'top-secret-key',
      });
      const redactedJson = redactText(jsonPayload);

      expect(redactedJson).toContain('"orderId":"O-20261123-0001"');
      expect(redactedJson).toContain(`"shippingAddress":"${REDACTED_ADDRESS}"`);
      expect(redactedJson).toContain(`"address":"${REDACTED_ADDRESS}"`);
      expect(redactedJson).toContain(`"street":"${REDACTED_ADDRESS}"`);
      expect(redactedJson).toContain(`"ward":"${REDACTED_ADDRESS}"`);
      expect(redactedJson).toContain(`"province":"${REDACTED_ADDRESS}"`);
      expect(redactedJson).toContain(`"recipient":"${REDACTED_VALUE}"`);
      expect(redactedJson).toContain(`"fullName":"${REDACTED_VALUE}"`);
      expect(redactedJson).toContain(`"password":"${REDACTED_VALUE}"`);
      expect(redactedJson).toContain(`"currentPassword":"${REDACTED_VALUE}"`);
      expect(redactedJson).toContain(`"newPassword":"${REDACTED_VALUE}"`);
      expect(redactedJson).toContain(`"token":"${REDACTED_VALUE}"`);
      expect(redactedJson).toContain(`"authorization":"${REDACTED_VALUE}"`);
      expect(redactedJson).toContain(`"secret":"${REDACTED_VALUE}"`);
      expect(redactedJson).not.toContain('123 Le Loi');
      expect(redactedJson).not.toContain('Lan Nguyen');
      expect(redactedJson).not.toContain('SuperSecretPassword1!');
      expect(redactedJson).not.toContain('jwt-token-value-12345');

      const kvLine =
        'shipping_address="123 Le Loi, Q1" full_name="Lan Nguyen" authorization=Bearer eyJhbGciOiJIUzI1NiJ9.abc.def secret=s3cr3t';
      const redactedKv = redactText(kvLine);

      expect(redactedKv).toBe(
        `shipping_address=${REDACTED_ADDRESS} full_name=${REDACTED_VALUE} authorization=${REDACTED_VALUE} secret=${REDACTED_VALUE}`,
      );
    });

    it('preserves order IDs, correlation IDs, ISO dates, and numeric metrics', () => {
      const safeLog =
        'orderId=O-20261123-0901234567 correlationId=req-0901234567abcdef date=2026-11-23 timestamp=2026-11-23T12:35:29Z status=200 durationMs=12.34';
      expect(redactText(safeLog)).toBe(safeLog);
    });
  });

  describe('redactObject', () => {
    it('recursively redacts nested objects, arrays, and free-text values without mutating input', () => {
      const input = {
        email: 'lan.nguyen@example.com',
        phoneNumber: '0901234567',
        fullName: 'Lan Nguyen',
        password: 'MyPassword123',
        accessToken: 'tok-abcdef123456',
        shippingAddress: {
          recipient: 'Lan Nguyen',
          line1: '123 Le Loi',
          street: 'Le Loi',
          ward: 'Ben Nghe',
          district: 'District 1',
          province: 'Ho Chi Minh City',
          phone: '+84901234567',
        },
        items: [
          {
            orderId: 'O-20261123-0001',
            note: 'Call 0912345678 or email lan.nguyen@example.com',
          },
        ],
        addresses: ['123 Le Loi', '456 Tran Hung Dao'],
        durationMs: 12.34,
        date: '2026-11-23',
      };

      const redacted = redactObject(input);

      expect(redacted).toEqual({
        email: 'l***@example.com',
        phoneNumber: '090****567',
        fullName: REDACTED_VALUE,
        password: REDACTED_VALUE,
        accessToken: REDACTED_VALUE,
        shippingAddress: {
          recipient: REDACTED_VALUE,
          line1: REDACTED_ADDRESS,
          street: REDACTED_ADDRESS,
          ward: REDACTED_ADDRESS,
          district: REDACTED_ADDRESS,
          province: REDACTED_ADDRESS,
          phone: '+84****567',
        },
        items: [
          {
            orderId: 'O-20261123-0001',
            note: 'Call 091****678 or email l***@example.com',
          },
        ],
        addresses: [REDACTED_ADDRESS, REDACTED_ADDRESS],
        durationMs: 12.34,
        date: '2026-11-23',
      });

      // Ensure original input is not mutated
      expect(input.email).toBe('lan.nguyen@example.com');
      expect(input.shippingAddress.line1).toBe('123 Le Loi');
    });

    it('returns undefined when input is undefined', () => {
      expect(redactObject(undefined)).toBeUndefined();
    });
  });
});
