import { INestApplication, UnprocessableEntityException, ValidationError, ValidationPipe } from '@nestjs/common';
import { randomBytes } from 'node:crypto';
import { NextFunction, Request, Response } from 'express';
import { ApiExceptionFilter } from './api-exception.filter';
import { ApiResponseInterceptor } from './api-response.interceptor';

function collectErrors(errors: ValidationError[], prefix = ''): Record<string, string[]> {
  const result: Record<string, string[]> = {};
  for (const error of errors) {
    const path = prefix ? `${prefix}.${error.property}` : error.property;
    if (error.constraints) result[path] = Object.values(error.constraints);
    Object.assign(result, collectErrors(error.children || [], path));
  }
  return result;
}

export function configureHttpFoundation(app: INestApplication): void {
  app.use((req: Request, res: Response, next: NextFunction) => {
    const supplied = req.header('X-Correlation-ID') || '';
    const id = /^[A-Za-z0-9._-]{1,128}$/.test(supplied) ? supplied : `req-${randomBytes(8).toString('hex')}`;
    req.headers['x-correlation-id'] = id;
    res.setHeader('X-Correlation-ID', id);
    next();
  });
  app.useGlobalFilters(new ApiExceptionFilter());
  app.useGlobalInterceptors(new ApiResponseInterceptor());
  app.useGlobalPipes(new ValidationPipe({
    transform: true,
    whitelist: true,
    forbidNonWhitelisted: true,
    exceptionFactory: (errors) => new UnprocessableEntityException({
      message: 'Request validation failed', code: 'VALIDATION_FAILED', errors: collectErrors(errors),
    }),
  }));
}
