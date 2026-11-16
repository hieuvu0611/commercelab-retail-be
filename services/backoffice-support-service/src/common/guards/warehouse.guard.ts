import {
  CanActivate,
  ExecutionContext,
  ForbiddenException,
  Injectable,
} from '@nestjs/common';
import { AuthenticatedUser } from './jwt-auth.guard';

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

    if (user.roles.includes('ADMIN') || user.warehouses.includes('*') || user.isInternal) {
      return true;
    }

    const targetWarehouse =
      req.params?.warehouseId || req.query?.warehouseId || req.body?.warehouseId;

    if (!targetWarehouse) {
      // If no specific warehouse requested on route, allow
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
