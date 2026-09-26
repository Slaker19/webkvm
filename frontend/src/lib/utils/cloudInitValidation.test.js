import { describe, it, expect } from 'vitest';
import {
  isSystemReservedUser,
  getCloudInitErrorKey,
  SYSTEM_RESERVED_USERS,
} from './cloudInitValidation.js';

describe('cloudInitValidation', () => {
  describe('isSystemReservedUser', () => {
    it('identifies system users correctly', () => {
      expect(isSystemReservedUser('root')).toBe(true);
      expect(isSystemReservedUser('ROOT')).toBe(true);
      expect(isSystemReservedUser('sudo')).toBe(true);
      expect(isSystemReservedUser('www-data')).toBe(true);
      expect(isSystemReservedUser('nobody')).toBe(true);
    });

    it('allows normal usernames', () => {
      expect(isSystemReservedUser('webkvm')).toBe(false);
      expect(isSystemReservedUser('demo')).toBe(false);
      expect(isSystemReservedUser('ubuntu')).toBe(false);
      expect(isSystemReservedUser('debian')).toBe(false);
    });

    it('exports a valid Set of reserved usernames', () => {
      expect(SYSTEM_RESERVED_USERS instanceof Set).toBe(true);
      expect(SYSTEM_RESERVED_USERS.has('root')).toBe(true);
      expect(SYSTEM_RESERVED_USERS.has('admin')).toBe(true);
      expect(SYSTEM_RESERVED_USERS.has('nobody')).toBe(true);
    });
  });

  describe('getCloudInitErrorKey', () => {
    it('returns empty string if cloud-init is disabled for VM', () => {
      expect(
        getCloudInitErrorKey({
          enabled: false,
          instanceType: 'vm',
          user: '',
          password: '',
        })
      ).toBe('');
    });

    it('requires password for container', () => {
      expect(
        getCloudInitErrorKey({
          enabled: true,
          instanceType: 'container',
          user: '',
          password: '',
        })
      ).toBe('vmCreate.ciPasswordRequired');
    });

    it('validates password length bounds for container', () => {
      expect(
        getCloudInitErrorKey({
          enabled: true,
          instanceType: 'container',
          user: '',
          password: '123',
        })
      ).toBe('vmCreate.ciPasswordMin');

      expect(
        getCloudInitErrorKey({
          enabled: true,
          instanceType: 'container',
          user: '',
          password: '123456789012345',
        })
      ).toBe('vmCreate.ciPasswordMax');

      expect(
        getCloudInitErrorKey({
          enabled: true,
          instanceType: 'container',
          user: '',
          password: 'validpass',
        })
      ).toBe('');
    });

    it('checks reserved username for container when user is provided', () => {
      expect(
        getCloudInitErrorKey({
          enabled: true,
          instanceType: 'container',
          user: 'root',
          password: 'validpass',
        })
      ).toBe('vmCreate.ciUserReserved');
    });

    it('validates cloudinit instance type similarly to container', () => {
      expect(
        getCloudInitErrorKey({
          enabled: false, // cloudinit type is always validated
          instanceType: 'cloudinit',
          user: '',
          password: '',
        })
      ).toBe('vmCreate.ciPasswordRequired');
    });

    it('requires username and password for standard VM', () => {
      expect(
        getCloudInitErrorKey({
          enabled: true,
          instanceType: 'vm',
          user: '',
          password: 'validpass',
        })
      ).toBe('vmCreate.ciUserRequired');

      expect(
        getCloudInitErrorKey({
          enabled: true,
          instanceType: 'vm',
          user: 'daemon',
          password: 'validpass',
        })
      ).toBe('vmCreate.ciUserReserved');

      expect(
        getCloudInitErrorKey({
          enabled: true,
          instanceType: 'vm',
          user: 'webkvm',
          password: '',
        })
      ).toBe('vmCreate.ciPasswordRequired');

      expect(
        getCloudInitErrorKey({
          enabled: true,
          instanceType: 'vm',
          user: 'webkvm',
          password: 'validpass',
        })
      ).toBe('');
    });
  });
});
