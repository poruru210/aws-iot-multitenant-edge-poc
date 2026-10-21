import { randomUUID } from 'node:crypto';
import type { APIGatewayProxyHandler } from 'aws-lambda';
import { IoTClient, CreateProvisioningClaimCommand, DescribeEndpointCommand, DescribeThingCommand } from '@aws-sdk/client-iot';
import { DynamoDBClient } from '@aws-sdk/client-dynamodb';
import { DynamoDBDocumentClient, PutCommand, TransactWriteCommand } from '@aws-sdk/lib-dynamodb';
import { validId, validHash } from './domain';

const iot = new IoTClient({ maxAttempts: 2 });
const db = DynamoDBDocumentClient.from(new DynamoDBClient({ maxAttempts: 2 }));
const table = process.env.TABLE_NAME!;
const reply = (statusCode: number, body: unknown) => ({ statusCode, headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }, body: JSON.stringify(body) });

export const handler: APIGatewayProxyHandler = async event => {
  const tenantId = event.pathParameters?.tenantId;
  const operator = event.requestContext.identity.userArn;
  if (!operator) return reply(403, { error: 'IAM-authenticated operator required' });
  if (!validId(tenantId) || !(JSON.parse(process.env.TENANTS!) as string[]).includes(tenantId)) return reply(403, { error: 'Unknown tenant' });
  let body: { thingName?: string; publicKeyHash?: string };
  try {
    const raw = event.isBase64Encoded ? Buffer.from(event.body ?? '', 'base64').toString('utf8') : event.body ?? '';
    if (raw.length > 4096) throw new Error('too large');
    body = JSON.parse(raw);
    if (!body || !validId(body.thingName) || !validHash(body.publicKeyHash)) throw new Error('invalid');
  } catch { return reply(400, { error: 'thingName and SHA-256 publicKeyHash are required' }); }
  const { thingName, publicKeyHash } = body;
  const reservationId = randomUUID();
  const now = Math.floor(Date.now() / 1000);
  try {
    // Do not silently take over an existing Thing, even if it is outside our ledger.
    try {
      await iot.send(new DescribeThingCommand({ thingName }));
      return reply(409, { error: 'Thing already exists; use an administrator-controlled replacement procedure' });
    } catch (err) {
      if ((err as Error).name !== 'ResourceNotFoundException') throw err;
    }
    await db.send(new PutCommand({
      TableName: table,
      Item: { pk: `DEVICE#${thingName}`, tenantId, thingName, publicKeyHash, reservationId, status: 'RESERVING', expiresAt: now + 360, operator },
      // An expired, never-approved reservation can be retried with the SAME key and tenant.
      ConditionExpression: 'attribute_not_exists(pk) OR (tenantId = :t AND publicKeyHash = :h AND expiresAt < :n AND attribute_not_exists(certificateId) AND (#s = :issued OR #s = :reserving))',
      ExpressionAttributeNames: { '#s': 'status' },
      ExpressionAttributeValues: { ':t': tenantId, ':h': publicKeyHash, ':n': now - 60, ':issued': 'ISSUED', ':reserving': 'RESERVING' },
    }));
    const [claim, data, credential] = await Promise.all([
      iot.send(new CreateProvisioningClaimCommand({ templateName: process.env.TEMPLATE_NAME })),
      iot.send(new DescribeEndpointCommand({ endpointType: 'iot:Data-ATS' })),
      iot.send(new DescribeEndpointCommand({ endpointType: 'iot:CredentialProvider' })),
    ]);
    if (!claim.certificateId || !claim.certificatePem || !claim.keyPair?.PrivateKey || !claim.expiration || !data.endpointAddress || !credential.endpointAddress) throw new Error('IncompleteAWSResponse');
    const claimId = claim.certificateId;
    const expiresAt = Math.floor(claim.expiration.getTime() / 1000);
    await db.send(new TransactWriteCommand({ TransactItems: [
      { Put: { TableName: table, Item: { pk: `CLAIM#${claimId}`, tenantId, thingName, publicKeyHash, claimId, expiresAt, ttl: expiresAt + 7 * 86400, status: 'ISSUED', operator }, ConditionExpression: 'attribute_not_exists(pk)' } },
      { Update: { TableName: table, Key: { pk: `DEVICE#${thingName}` }, UpdateExpression: 'SET claimId = :c, #s = :s, expiresAt = :e', ConditionExpression: 'reservationId = :r AND #s = :old', ExpressionAttributeNames: { '#s': 'status' }, ExpressionAttributeValues: { ':c': claimId, ':s': 'ISSUED', ':e': expiresAt, ':r': reservationId, ':old': 'RESERVING' } } },
    ] }));
    console.info(JSON.stringify({ action: 'claim-issued', tenantId, thingName, operator, claimId }));
    // The claim key exists only in this response, never in the ledger or logs.
    return reply(201, {
      version: 1, tenantId, thingName, publicKeyHash, claimId,
      certificatePem: claim.certificatePem, privateKey: claim.keyPair.PrivateKey,
      expiration: claim.expiration.toISOString(),
      region: process.env.AWS_REGION, dataEndpoint: data.endpointAddress,
      credentialEndpoint: credential.endpointAddress, templateName: process.env.TEMPLATE_NAME,
      roleAlias: process.env.ROLE_ALIAS, bucket: process.env.BUCKET_NAME,
    });
  } catch (err) {
    const name = (err as Error).name;
    console.warn(JSON.stringify({ action: 'claim-failed', tenantId, thingName, errorType: name }));
    // Never blindly delete a reservation: it might already have reached APPROVED.
    return reply(['ConditionalCheckFailedException', 'TransactionCanceledException'].includes(name) ? 409 : 500,
      { error: 'Claim not issued. Inspect the ledger; retry an unapproved expired reservation with the same key after the grace period.' });
  }
};
