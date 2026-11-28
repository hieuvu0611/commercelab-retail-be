import { INestApplication, UnprocessableEntityException, ValidationError, ValidationPipe } from '@nestjs/common';
import { randomBytes } from 'node:crypto';
import { NextFunction, Request, Response } from 'express';
import { ApiExceptionFilter } from './api-exception.filter';
import { ApiResponseInterceptor } from './api-response.interceptor';
import { redactText } from '../privacy/pii-redactor';
import {
  defaultObservabilityRegistry,
  resolveTraceContext,
} from '../../modules/observability/observability.service';

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

    const trace = resolveTraceContext(req.header('traceparent'));
    req.headers['traceparent'] = trace.traceparent;
    res.setHeader('traceparent', trace.traceparent);

    const started = process.hrtime.bigint();
    res.on('finish', () => {
      const elapsedNs = Number(process.hrtime.bigint() - started);
      if (req.path !== '/metrics') {
        defaultObservabilityRegistry.recordHttpRequest(req.method, req.path, res.statusCode, elapsedNs / 1e9);
      }
      if ((req.path === '/health' || req.path === '/metrics') && res.statusCode < 400) return;
      const durationMs = (elapsedNs / 1e6).toFixed(2);
      console.log(
        `http method=${req.method} path=${redactText(req.path)} status=${res.statusCode} durationMs=${durationMs} correlationId=${id} traceId=${trace.traceId}`,
      );
    });
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
