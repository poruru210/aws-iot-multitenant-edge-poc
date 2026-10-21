import { DynamoDBClient } from '@aws-sdk/client-dynamodb';
import { DynamoDBDocumentClient, GetCommand, TransactWriteCommand } from '@aws-sdk/lib-dynamodb';
import { allowed, certificateKeyHash, type HookInput, type Ticket } from './domain';

const db = DynamoDBDocumentClient.from(new DynamoDBClient({ maxAttempts: 1 }));
const table = process.env.TABLE_NAME!;
const denied = { allowProvisioning: false, parameterOverrides: {} };

export async function handler(event: HookInput) {
  const now = Math.floor(Date.now() / 1000);
  const options = { abortSignal: AbortSignal.timeout(3500) };
  try {
    if (!event.claimCertificateId || !event.certificatePem || event.templateArn !== process.env.TEMPLATE_ARN) return denied;
    const result = await db.send(new GetCommand({ TableName: table, Key: { pk: `CLAIM#${event.claimCertificateId}` }, ConsistentRead: true }), options);
    const ticket = result.Item as Ticket | undefined;
    if (!allowed(ticket, event, certificateKeyHash(event.certificatePem), now, process.env.TEMPLATE_ARN!)) return denied;
    const t = ticket!;
    // Both records participate in one transaction; parallel requests cannot bind
    // one claim/device to two different certificates. Same-certificate retries work.
    await db.send(new TransactWriteCommand({ TransactItems: [
      { Update: {
        TableName: table, Key: { pk: `CLAIM#${t.claimId}` },
        UpdateExpression: 'SET certificateId = :c, #s = :approved',
        ConditionExpression: 'expiresAt > :now AND (#s = :issued OR #s = :approved) AND (attribute_not_exists(certificateId) OR certificateId = :c)',
        ExpressionAttributeNames: { '#s': 'status' },
        ExpressionAttributeValues: { ':c': event.certificateId, ':now': now, ':issued': 'ISSUED', ':approved': 'APPROVED' },
      } },
      { Update: {
        TableName: table, Key: { pk: `DEVICE#${t.thingName}` },
        UpdateExpression: 'SET certificateId = :c, #s = :approved',
        ConditionExpression: 'claimId = :claim AND tenantId = :tenant AND publicKeyHash = :hash AND (#s = :issued OR #s = :approved) AND (attribute_not_exists(certificateId) OR certificateId = :c)',
        ExpressionAttributeNames: { '#s': 'status' },
        ExpressionAttributeValues: { ':c': event.certificateId, ':claim': t.claimId, ':tenant': t.tenantId, ':hash': t.publicKeyHash, ':issued': 'ISSUED', ':approved': 'APPROVED' },
      } },
    ] }), options);
    console.info(JSON.stringify({ action: 'registration-approved', tenantId: t.tenantId, thingName: t.thingName, certificateId: event.certificateId }));
    return { allowProvisioning: true, parameterOverrides: { TenantId: t.tenantId, ThingName: t.thingName } };
  } catch (err) {
    console.warn(JSON.stringify({ action: 'registration-denied', errorType: (err as Error).name }));
    return denied;
  }
}
