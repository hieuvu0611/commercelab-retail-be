import { ArgumentsHost, Catch, ExceptionFilter, HttpException, HttpStatus, Logger } from '@nestjs/common';
import { Request, Response } from 'express';
import { redactText } from '../privacy/pii-redactor';

const codes: Record<number, string> = {
  400: 'INVALID_REQUEST', 401: 'UNAUTHENTICATED', 403: 'FORBIDDEN',
  404: 'NOT_FOUND', 409: 'CONFLICT', 413: 'PAYLOAD_TOO_LARGE',
  415: 'UNSUPPORTED_MEDIA_TYPE', 422: 'VALIDATION_FAILED', 429: 'RATE_LIMIT_EXCEEDED',
  503: 'SERVICE_UNAVAILABLE',
};

@Catch()
export class ApiExceptionFilter implements ExceptionFilter {
  private readonly logger = new Logger(ApiExceptionFilter.name);

  catch(exception: unknown, host: ArgumentsHost): void {
    const context = host.switchToHttp();
    const request = context.getRequest<Request>();
    const response = context.getResponse<Response>();
    if (response.headersSent) {
      this.logger.error(redactText(`Response already sent; correlationId=${request.headers['x-correlation-id']}`));
      response.destroy();
      return;
    }
    const status = exception instanceof HttpException ? exception.getStatus() : HttpStatus.INTERNAL_SERVER_ERROR;
    const payload = exception instanceof HttpException ? exception.getResponse() : undefined;
    const body = payload && typeof payload === 'object' ? payload as Record<string, unknown> : {};
    if (status >= 500) {
      const errorMessage = redactText(`Request failed; correlationId=${request.headers['x-correlation-id']}`);
      const errorStack = exception instanceof Error && exception.stack ? redactText(exception.stack) : undefined;
      this.logger.error(errorMessage, errorStack);
    }
    const preserveSafe503 = status === HttpStatus.SERVICE_UNAVAILABLE && body.expose === true;
    const message = status >= 500 && !preserveSafe503
      ? (status === HttpStatus.SERVICE_UNAVAILABLE ? 'Service unavailable' : 'Internal server error')
      : typeof body.message === 'string' ? body.message : typeof payload === 'string' ? payload : 'Request failed';
    const errors = status === 422 && body.errors && typeof body.errors === 'object' ? body.errors : undefined;
    response.status(status).json({
      message, code: status === HttpStatus.SERVICE_UNAVAILABLE ? 'SERVICE_UNAVAILABLE' : status >= 500 ? 'INTERNAL_ERROR' : typeof body.code === 'string' ? body.code : codes[status] || 'HTTP_ERROR',
      status, correlationId: request.headers['x-correlation-id'], ...(errors ? { errors } : {}),
    });
  }
}
