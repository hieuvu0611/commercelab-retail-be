import { CallHandler, ExecutionContext, Injectable, NestInterceptor } from '@nestjs/common';
import { Observable, map } from 'rxjs';

export class ApiResult<T> {
  constructor(
    readonly data: T,
    readonly message?: string,
    readonly metadata?: { total: number; page: number; pageCount: number; limit: number },
  ) {}
}

@Injectable()
export class ApiResponseInterceptor implements NestInterceptor {
  intercept(context: ExecutionContext, next: CallHandler): Observable<unknown> {
    return next.handle().pipe(map((value: unknown) => {
      if (context.switchToHttp().getResponse().statusCode === 204) return undefined;
      if (value instanceof ApiResult) {
        return { data: value.data, ...(value.message ? { message: value.message } : {}), ...(value.metadata ? { metadata: value.metadata } : {}) };
      }
      return { data: value === undefined ? null : value };
    }));
  }
}
