import {
  CanActivate,
  ExecutionContext,
  ForbiddenException,
  Injectable,
  Optional,
} from '@nestjs/common';
import { Reflector } from '@nestjs/core';
import { AuthenticatedUser } from './jwt-auth.guard';
import { ROLES_KEY } from './roles.decorator';

export const READ_CROSS_WAREHOUSE_ROLES = new Set([
  'ADMIN',
  'OPS_MGR',
  'CSKH',
  'SUPPORT_AGENT',
  'SUPPORT_STAFF',
  'SUPPORT_LEAD',
  'FINANCE',
  'FINANCE_MGR',
]);

export const WAREHOUSE_MUTATION_CROSS_ROLES = new Set(['ADMIN', 'OPS_MGR']);

export function hasScopedWarehouseRole(user?: AuthenticatedUser): boolean {
  if (!user) return false;
  const isWarehouseRole = user.roles.some(
    (role) => role === 'WAREHOUSE_STAFF' || role === 'WAREHOUSE_MGR',
  );
  if (!isWarehouseRole) return false;
  return !user.warehouses.includes('*') && !user.warehouses.includes('ALL');
}

export function hasAllWarehouseScope(
  user?: AuthenticatedUser,
  crossWarehouseRoles: ReadonlySet<string> = READ_CROSS_WAREHOUSE_ROLES,
): boolean {
  if (!user) return false;
  if (
    user.isInternal === true ||
    user.warehouses.includes('*') ||
    user.warehouses.includes('ALL')
  ) {
    return true;
  }
  if (hasScopedWarehouseRole(user)) {
    return false;
  }
  return user.roles.some((role) => crossWarehouseRoles.has(role));
}

export function canAccessWarehouse(
  user: AuthenticatedUser | undefined,
  warehouseId?: string,
  crossWarehouseRoles: ReadonlySet<string> = READ_CROSS_WAREHOUSE_ROLES,
): boolean {
  if (!user) return false;
  if (!warehouseId) return true;
  if (hasAllWarehouseScope(user, crossWarehouseRoles)) return true;
  return user.warehouses.includes(warehouseId);
}

@Injectable()
export class WarehouseScopeGuard implements CanActivate {
  constructor(@Optional() private readonly reflector?: Reflector) {}

  canActivate(context: ExecutionContext): boolean {
    const req = context.switchToHttp().getRequest();
    const user = req.user as AuthenticatedUser | undefined;

    if (!user) {
      throw new ForbiddenException({
        code: 'FORBIDDEN',
        message: 'No authenticated user found for warehouse scope check',
      });
    }

    const routeRoles = this.reflector?.getAllAndOverride<string[]>(ROLES_KEY, [
      context.getHandler(),
      context.getClass(),
    ]);
    const allowedCrossRoles = routeRoles?.length
      ? new Set(routeRoles.filter((role) => READ_CROSS_WAREHOUSE_ROLES.has(role)))
      : WAREHOUSE_MUTATION_CROSS_ROLES;

    if (hasAllWarehouseScope(user, allowedCrossRoles)) {
      return true;
    }

    const targetWarehouse =
      req.params?.warehouseId || req.query?.warehouseId || req.body?.warehouseId;

    if (!targetWarehouse) {
      // Handler will verify entity warehouseId when loading by resource id
      return true;
    }

    if (!user.warehouses.includes(targetWarehouse)) {
      throw new ForbiddenException({
        code: 'FORBIDDEN',
        message: `User warehouse scope does not grant access to ${targetWarehouse}`,
      });
    }

    return true;
  }
}
