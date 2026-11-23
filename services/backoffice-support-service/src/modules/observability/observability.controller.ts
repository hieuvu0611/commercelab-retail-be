import { Controller, Get, Res } from '@nestjs/common';
import { Response } from 'express';
import { ObservabilityService } from './observability.service';

@Controller()
export class ObservabilityController {
  constructor(private readonly observability: ObservabilityService) {}

  @Get('/metrics')
  async metrics(@Res() res: Response): Promise<void> {
    const body = await this.observability.renderMetrics();
    res.setHeader('Content-Type', 'text/plain; version=0.0.4; charset=utf-8');
    res.status(200).send(body);
  }
}
