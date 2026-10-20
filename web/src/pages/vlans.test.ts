import { describe, expect, it } from 'vitest';
import { parseVlans, vlanName } from './Vlans';

describe('VLAN list preview (mirrors hostnet.ParseVLANs)', () => {
  it('parses lists and ranges into sorted unique IDs', () => {
    expect(parseVlans('30-32, 10;10 4094')).toEqual([10, 30, 31, 32, 4094]);
  });
  it('reports the first bad item', () => {
    expect(parseVlans('10, 0')).toBe('0');
    expect(parseVlans('5-3')).toBe('5-3');
    expect(parseVlans('4095')).toBe('4095');
    expect(parseVlans('1-100')).toBe('limit');
  });
  it('names interfaces like the agent', () => {
    expect(vlanName('dotted', 'eth1', 20)).toBe('eth1.20');
    expect(vlanName('dotted', 'enp129s0f1np1', 20)).toBe('vlan20');
    expect(vlanName('vlan', 'eth1', 20)).toBe('vlan20');
  });
});
