import { Injectable, OnModuleDestroy, OnModuleInit } from '@nestjs/common';
import { Pool, PoolClient, QueryResult } from 'pg';

@Injectable()
export class DatabaseService implements OnModuleInit, OnModuleDestroy {
  private pool: Pool | null = null;

  onModuleInit() {
    const connectionString =
      process.env.DATABASE_URL_SUPPORT ||
      'postgres://support_svc_user:support_secret@localhost:5432/commerce_lab?sslmode=disable&search_path=support';

    this.pool = new Pool({
      connectionString,
      max: 10,
      idleTimeoutMillis: 30000,
      connectionTimeoutMillis: 5000,
    });

    this.pool.on('error', (err) => {
      console.error('[database-service] Unexpected pg client error:', err.message);
    });
  }

  async onModuleDestroy() {
    if (this.pool) {
      await this.pool.end();
      this.pool = null;
    }
  }

  async query<T extends Record<string, any> = any>(
    text: string,
    params?: any[],
  ): Promise<QueryResult<T>> {
    if (!this.pool) {
      throw new Error('Database pool is not initialized');
    }
    return this.pool.query<T>(text, params);
  }

  async ping(): Promise<boolean> {
    if (!this.pool) return false;
    try {
      const res = await this.pool.query('SELECT 1 as ok');
      return res.rows[0]?.ok === 1;
    } catch {
      return false;
    }
  }

  async transaction<T>(work: (client: PoolClient) => Promise<T>): Promise<T> {
    if (!this.pool) {
      throw new Error('Database pool is not initialized');
    }
    const client = await this.pool.connect();
    let releaseError: Error | undefined;
    try {
      await client.query('BEGIN');
      const result = await work(client);
      await client.query('COMMIT');
      return result;
    } catch (error) {
      try {
        await client.query('ROLLBACK');
      } catch (rollbackError) {
        releaseError = new AggregateError([error, rollbackError], 'Database transaction and rollback failed');
        throw releaseError;
      }
      throw error;
    } finally {
      client.release(releaseError);
    }
  }

  getPool(): Pool | null {
    return this.pool;
  }
}
