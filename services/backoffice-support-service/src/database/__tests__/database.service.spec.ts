import { DatabaseService } from '../database.service';

describe('DatabaseService', () => {
  let service: DatabaseService;

  beforeEach(() => {
    service = new DatabaseService();
  });

  afterEach(async () => {
    await service.onModuleDestroy();
  });

  it('initializes a pool on module init and destroys on module destroy', async () => {
    service.onModuleInit();
    const pool = service.getPool();
    expect(pool).toBeDefined();

    await service.onModuleDestroy();
    expect(service.getPool()).toBeNull();
  });

  it('returns false for ping when database is not reachable', async () => {
    process.env.DATABASE_URL_SUPPORT = 'postgres://invalid:invalid@localhost:9999/invalid';
    service.onModuleInit();
    const ok = await service.ping();
    expect(ok).toBe(false);
  });
});
