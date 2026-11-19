import {
  CanActivate,
  ExecutionContext,
  Injectable,
  UnauthorizedException,
} from '@nestjs/common';
import * as jwt from 'jsonwebtoken';

export interface AuthenticatedUser {
  userId: string;
  roles: string[];
  warehouses: string[];
  isInternal?: boolean;
}

const DEFAULT_DEV_JWT_SECRET = 'dev-only-jwt-access-secret-never-use-in-production';
const DEFAULT_DEV_INTERNAL_TOKEN = 'dev-only-internal-service-token-never-use';
const MIN_SECRET_BYTES = 32;

export function isDevEnv(): boolean {
  return process.env.APP_ENV === 'dev';
}

export function resolveJwtAccessSecret(strictLength = false): string {
  const secret = process.env.JWT_ACCESS_SECRET || '';
  if (!secret && isDevEnv()) {
    return DEFAULT_DEV_JWT_SECRET;
  }
  if (!secret) {
    throw new Error('JWT_ACCESS_SECRET is required outside APP_ENV=dev');
  }
  if (strictLength && secret.length < MIN_SECRET_BYTES && !isDevEnv()) {
    throw new Error(`JWT_ACCESS_SECRET must be at least ${MIN_SECRET_BYTES} bytes`);
  }
  return secret;
}

export function resolveInternalServiceToken(strictLength = false): string {
  const token = process.env.INTERNAL_SERVICE_TOKEN || '';
  if (!token && isDevEnv()) {
    return DEFAULT_DEV_INTERNAL_TOKEN;
  }
  if (!token) {
    throw new Error('INTERNAL_SERVICE_TOKEN is required outside APP_ENV=dev');
  }
  if (strictLength && token.length < MIN_SECRET_BYTES && !isDevEnv()) {
    throw new Error(`INTERNAL_SERVICE_TOKEN must be at least ${MIN_SECRET_BYTES} bytes`);
  }
  return token;
}

export function validateAuthSecrets(): void {
  resolveJwtAccessSecret(true);
  resolveInternalServiceToken(true);
}

@Injectable()
export class JwtAuthGuard implements CanActivate {
  private getSecret(): string {
    return resolveJwtAccessSecret(false);
  }

  private getInternalToken(): string {
    return resolveInternalServiceToken(false);
  }

  canActivate(context: ExecutionContext): boolean {
    const req = context.switchToHttp().getRequest();
    const authHeader = req.headers['authorization'];
    const internalHeader = req.headers['x-internal-token'];

    // 1. Allow internal service-to-service calls if internal token matches
    if (internalHeader) {
      let expectedInternal = '';
      try {
        expectedInternal = this.getInternalToken();
      } catch {
        throw new UnauthorizedException({
          code: 'UNAUTHORIZED',
          message: 'Internal authentication is not configured',
        });
      }
      if (internalHeader === expectedInternal) {
        req.user = {
          userId: 'system:internal',
          roles: ['ADMIN', 'INTERNAL_SERVICE'],
          warehouses: ['*'],
          isInternal: true,
        };
        return true;
      }
    }

    // 2. Validate Bearer token
    if (!authHeader || typeof authHeader !== 'string' || !authHeader.startsWith('Bearer ')) {
      throw new UnauthorizedException({
        code: 'UNAUTHORIZED',
        message: 'Missing or malformed Authorization header',
      });
    }

    const token = authHeader.slice(7).trim();
    if (!token) {
      throw new UnauthorizedException({
        code: 'UNAUTHORIZED',
        message: 'Empty Bearer token',
      });
    }

    let secret: string;
    try {
      secret = this.getSecret();
    } catch {
      throw new UnauthorizedException({
        code: 'UNAUTHORIZED',
        message: 'JWT verification secret is not configured',
      });
    }

    try {
      const decoded = jwt.verify(token, secret) as jwt.JwtPayload;
      const roles: string[] = Array.isArray(decoded.roles)
        ? decoded.roles
        : typeof decoded.role === 'string'
        ? [decoded.role]
        : [];

      const rawWarehouses = Array.isArray(decoded.warehouseScopes)
        ? decoded.warehouseScopes
        : Array.isArray(decoded.warehouses)
        ? decoded.warehouses
        : typeof decoded.warehouse === 'string'
        ? [decoded.warehouse]
        : [];
      const warehouses: string[] = rawWarehouses.filter(
        (w): w is string => typeof w === 'string' && w.length > 0,
      );

      req.user = {
        userId: decoded.userId || decoded.sub || 'anonymous',
        roles,
        warehouses,
        isInternal: false,
      };
      return true;
    } catch (err: any) {
      throw new UnauthorizedException({
        code: 'UNAUTHORIZED',
        message: err.name === 'TokenExpiredError' ? 'Token expired' : 'Invalid token signature',
      });
    }
  }
}
