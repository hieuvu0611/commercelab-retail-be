import { ExecutionContext, ForbiddenException, UnauthorizedException } from '@nestjs/common';
import { Reflector } from '@nestjs/core';
import * as jwt from 'jsonwebtoken';
import { JwtAuthGuard } from '../jwt-auth.guard';
import { RolesGuard } from '../roles.guard';
import { WarehouseScopeGuard } from '../warehouse.guard';

describe('Auth Guards', () => {
  const secret = 'test-secret-at-least-32-bytes-long-for-testing';
  const oldSecret = process.env.JWT_ACCESS_SECRET;
  const oldInternal = process.env.INTERNAL_SERVICE_TOKEN;

  beforeAll(() => {
    process.env.JWT_ACCESS_SECRET = secret;
    process.env.INTERNAL_SERVICE_TOKEN = 'test-internal-token-secret-xyz';
  });

  afterAll(() => {
    process.env.JWT_ACCESS_SECRET = oldSecret;
    process.env.INTERNAL_SERVICE_TOKEN = oldInternal;
  });

  const mockContext = (req: any): ExecutionContext =>
    ({
      switchToHttp: () => ({
        getRequest: () => req,
        getResponse: () => ({}),
      }),
      getHandler: () => ({}),
      getClass: () => ({}),
    } as any);

  describe('JwtAuthGuard', () => {
    let guard: JwtAuthGuard;

    beforeEach(() => {
      guard = new JwtAuthGuard();
    });

    it('rejects request without Authorization header', () => {
      const ctx = mockContext({ headers: {} });
      expect(() => guard.canActivate(ctx)).toThrow(UnauthorizedException);
    });

    it('rejects invalid token signature', () => {
      const wrongSecretToken = jwt.sign({ sub: 'user-1' }, 'wrong-secret');
      const ctx = mockContext({
        headers: { authorization: `Bearer ${wrongSecretToken}` },
      });
      expect(() => guard.canActivate(ctx)).toThrow(UnauthorizedException);
    });

    it('accepts valid Bearer token and attaches user', () => {
      const token = jwt.sign(
        { sub: 'user-lan', roles: ['SUPPORT_STAFF'], warehouses: ['WH-HCM-01'] },
        secret,
      );
      const req: any = { headers: { authorization: `Bearer ${token}` } };
      const ctx = mockContext(req);

      const allowed = guard.canActivate(ctx);
      expect(allowed).toBe(true);
      expect(req.user).toEqual({
        userId: 'user-lan',
        roles: ['SUPPORT_STAFF'],
        warehouses: ['WH-HCM-01'],
        isInternal: false,
      });
    });

    it('accepts valid X-Internal-Token', () => {
      const req: any = {
        headers: { 'x-internal-token': 'test-internal-token-secret-xyz' },
      };
      const ctx = mockContext(req);

      const allowed = guard.canActivate(ctx);
      expect(allowed).toBe(true);
      expect(req.user.isInternal).toBe(true);
      expect(req.user.roles).toContain('ADMIN');
    });
  });

  describe('RolesGuard', () => {
    let reflector: Reflector;
    let guard: RolesGuard;

    beforeEach(() => {
      reflector = new Reflector();
      guard = new RolesGuard(reflector);
    });

    it('allows when no roles are required', () => {
      jest.spyOn(reflector, 'getAllAndOverride').mockReturnValue(undefined);
      const ctx = mockContext({ user: { roles: [] } });
      expect(guard.canActivate(ctx)).toBe(true);
    });

    it('allows when user has required role', () => {
      jest.spyOn(reflector, 'getAllAndOverride').mockReturnValue(['SUPPORT_STAFF']);
      const ctx = mockContext({ user: { roles: ['SUPPORT_STAFF', 'OTHER'] } });
      expect(guard.canActivate(ctx)).toBe(true);
    });

    it('allows ADMIN regardless of required roles', () => {
      jest.spyOn(reflector, 'getAllAndOverride').mockReturnValue(['WAREHOUSE_MGR']);
      const ctx = mockContext({ user: { roles: ['ADMIN'] } });
      expect(guard.canActivate(ctx)).toBe(true);
    });

    it('throws ForbiddenException when role is missing', () => {
      jest.spyOn(reflector, 'getAllAndOverride').mockReturnValue(['WAREHOUSE_MGR']);
      const ctx = mockContext({ user: { roles: ['SUPPORT_STAFF'] } });
      expect(() => guard.canActivate(ctx)).toThrow(ForbiddenException);
    });
  });

  describe('WarehouseScopeGuard', () => {
    let guard: WarehouseScopeGuard;

    beforeEach(() => {
      guard = new WarehouseScopeGuard();
    });

    it('allows when user has wildcard warehouse scope', () => {
      const ctx = mockContext({
        user: { roles: ['WAREHOUSE_STAFF'], warehouses: ['*'] },
        params: { warehouseId: 'WH-HN-01' },
      });
      expect(guard.canActivate(ctx)).toBe(true);
    });

    it('allows when user has matching warehouse scope', () => {
      const ctx = mockContext({
        user: { roles: ['WAREHOUSE_STAFF'], warehouses: ['WH-HCM-01'] },
        params: { warehouseId: 'WH-HCM-01' },
      });
      expect(guard.canActivate(ctx)).toBe(true);
    });

    it('throws ForbiddenException when out of warehouse scope', () => {
      const ctx = mockContext({
        user: { roles: ['WAREHOUSE_STAFF'], warehouses: ['WH-HCM-01'] },
        params: { warehouseId: 'WH-HN-01' },
      });
      expect(() => guard.canActivate(ctx)).toThrow(ForbiddenException);
    });
  });
});
