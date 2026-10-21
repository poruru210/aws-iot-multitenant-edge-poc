import test from 'node:test';
import assert from 'node:assert/strict';
import { App } from 'aws-cdk-lib';
import { Template, Match } from 'aws-cdk-lib/assertions';
import { EdgeStack } from '../lib/edge-stack';

test('IAM, S3 and fleet provisioning security invariants', () => {
  const stack = new EdgeStack(new App(), 'Test', { env: { account: '111122223333', region: 'ap-northeast-1' } });
  const t = Template.fromStack(stack);
  t.resourceCountIs('AWS::IoT::ThingType', 2);
  t.hasResourceProperties('AWS::IoT::RoleAlias', { CredentialDurationSeconds: 900 });
  t.hasResourceProperties('AWS::S3::Bucket', { PublicAccessBlockConfiguration: { BlockPublicAcls: true, BlockPublicPolicy: true, IgnorePublicAcls: true, RestrictPublicBuckets: true }, LifecycleConfiguration: { Rules: Match.arrayWith([Match.objectLike({ AbortIncompleteMultipartUpload: { DaysAfterInitiation: 1 } })]) } });
  t.hasResourceProperties('AWS::ApiGateway::Method', { HttpMethod: 'POST', AuthorizationType: 'AWS_IAM' });
  const body = JSON.stringify(t.toJSON());
  for (const required of ['credentials-iot:ThingTypeName', 'credentials-iot:ThingName', 'EXCLUSIVE_THING', 'Null', 'aws:SourceArn', 'aws:SourceAccount']) assert.ok(body.includes(required), required);
  assert.equal(body.includes('s3:*'), false);
  assert.equal(body.includes('s3:GetObject'), false);
  assert.equal(body.includes('s3:DeleteObject'), false);
  t.hasResource('AWS::S3::Bucket', { DeletionPolicy: 'Retain' });
  t.hasResource('AWS::DynamoDB::Table', { DeletionPolicy: 'Retain' });
});

test('reject unsafe tenant IDs before synthesis', () => {
  assert.throws(() => new EdgeStack(new App({ context: { tenants: ['../other'] } }), 'Unsafe'));
});
