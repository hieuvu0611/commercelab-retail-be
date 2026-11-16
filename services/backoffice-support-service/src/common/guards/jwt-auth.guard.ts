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

const DEFAULT_DEV_JWT_SECRET = 'dev-access-secret-at-least-32-bytes-long';
const DEFAULT_INTERNAL_TOKEN = 'dev-internal-token-at-least-32-bytes-long';

@Injectable()
export class JwtAuthGuard implements CanActivate {
  private getSecret(): string {
    return process.env.JWT_ACCESS_SECRET || DEFAULT_DEV_JWT_SECRET;
  }

  private getInternalToken(): string {
    return process.env.INTERNAL_SERVICE_TOKEN || DEFAULT_INTERNAL_TOKEN;
  }

  canActivate(context: ExecutionContext): boolean {
    const req = context.switchToHttp().getRequest();
    const authHeader = req.headers['authorization'];
    const internalHeader = req.headers['x-internal-token'];

    // 1. Allow internal service-to-service calls if internal token matches
    if (internalHeader && internalHeader === this.getInternalToken()) {
      req.user = {
        userId: 'system:internal',
        roles: ['ADMIN', 'INTERNAL_SERVICE'],
        warehouses: ['*'],
        isInternal: true,
      };
      return true;
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

    try {
      const decoded = jwt.verify(token, this.getSecret()) as jwt.JwtPayload;
      const roles: string[] = Array.isArray(decoded.roles)
        ? decoded.roles
        : typeof decoded.role === 'string'
        ? [decoded.role]
        : [];

      const warehouses: string[] = Array.isArray(decoded.warehouses)
        ? decoded.warehouses
        : typeof decoded.warehouse === 'string'
        ? [decoded.warehouse]
        : [];

      req.user = {
        userId: decoded.sub || decoded.userId || 'anonymous',
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
