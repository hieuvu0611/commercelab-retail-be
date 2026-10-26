import { NestFactory } from '@nestjs/core';
import { AppModule } from './app.module';

async function bootstrap() {
  const app = await NestFactory.create(AppModule);
  app.enableCors();
  const port = process.env.BACKOFFICE_SUPPORT_PORT || 8088;
  await app.listen(port);
  console.log(`[backoffice-support-service] NestJS listening on :${port} (schema: support, mongo: support_exception_cases)`);
}

bootstrap();
