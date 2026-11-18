import {
  CanActivate,
  ExecutionContext,
  ForbiddenException,
  Injectable,
} from '@nestjs/common';
import { AuthenticatedUser } from './jwt-auth.guard';

export function hasAllWarehouseScope(user?: AuthenticatedUser): boolean {
  if (!user) return false;
  return (
    user.roles.includes('ADMIN') ||
    user.roles.includes('OPS_MGR') ||
    user.isInternal === true ||
    user.warehouses.includes('*') ||
    user.warehouses.includes('ALL')
  );
}

export function canAccessWarehouse(user: AuthenticatedUser | undefined, warehouseId?: string): boolean {
  if (!user) return false;
  if (!warehouseId) return true;
  if (hasAllWarehouseScope(user)) return true;
  return user.warehouses.includes(warehouseId);
}

@Injectable()
export class WarehouseScopeGuard implements CanActivate {
  canActivate(context: ExecutionContext): boolean {
    const req = context.switchToHttp().getRequest();
    const user = req.user as AuthenticatedUser | undefined;

    if (!user) {
      throw new ForbiddenException({
        code: 'FORBIDDEN',
        message: 'No authenticated user found for warehouse scope check',
      });
    }

    if (hasAllWarehouseScope(user)) {
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
