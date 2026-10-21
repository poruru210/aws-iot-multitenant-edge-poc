import { createHash, X509Certificate } from 'node:crypto';

export function validId(value: unknown): value is string {
  return typeof value === 'string' && /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(value);
}
export function validHash(value: unknown): value is string {
  return typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
}
export function certificateKeyHash(pem: string): string {
  const cert = new X509Certificate(pem);
  return createHash('sha256').update(cert.publicKey.export({ type: 'spki', format: 'der' })).digest('hex');
}
export interface Ticket {
  tenantId: string; thingName: string; publicKeyHash: string;
  claimId: string; expiresAt: number; certificateId?: string; status: string;
}
export interface HookInput {
  claimCertificateId: string; certificateId: string; certificatePem: string; templateArn: string;
  parameters?: Record<string, string>;
}
// This pure predicate is followed by a conditional DynamoDB transaction. Never
// authorize from event.parameters: those values are controlled by the device.
export function allowed(ticket: Ticket | undefined, event: HookInput, keyHash: string, now: number, templateArn: string): boolean {
  return !!ticket && event.templateArn === templateArn
    && validId(ticket.tenantId) && validId(ticket.thingName)
    && validHash(ticket.publicKeyHash) && validHash(event.certificateId)
    && ticket.claimId === event.claimCertificateId
    && ticket.expiresAt > now && ticket.publicKeyHash === keyHash
    && ['ISSUED', 'APPROVED'].includes(ticket.status)
    && (!ticket.certificateId || ticket.certificateId === event.certificateId);
}
