import { NestFactory } from '@nestjs/core';
import { AppModule } from './app.module';
import { configureHttpFoundation } from './common/http/http-foundation';
import { validateAuthSecrets } from './common/guards/jwt-auth.guard';

async function bootstrap() {
  validateAuthSecrets();
  const app = await NestFactory.create(AppModule);
  configureHttpFoundation(app);
  app.enableCors();
  const port = process.env.BACKOFFICE_SUPPORT_PORT || 8088;
  await app.listen(port);
  console.log(`[backoffice-support-service] NestJS listening on :${port} (schema: support, mongo: support_exception_cases)`);
}

bootstrap();
