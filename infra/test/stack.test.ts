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
  // S3's TLS-only bucket policy legitimately uses Deny s3:*. Inspect
  // identity-policy Allow statements rather than rejecting the secure Deny.
  for (const type of ['AWS::IAM::Policy', 'AWS::IAM::ManagedPolicy']) {
    for (const resource of Object.values(t.findResources(type))) {
      const statements = resource.Properties.PolicyDocument.Statement as Array<{Effect: string; Action: string | string[]}>;
      for (const statement of statements.filter(s => s.Effect === 'Allow')) {
        const actions = Array.isArray(statement.Action) ? statement.Action : [statement.Action];
        for (const forbidden of ['s3:*', 's3:GetObject', 's3:DeleteObject']) assert.equal(actions.includes(forbidden), false);
      }
    }
  }
  t.hasResourceProperties('AWS::S3::BucketPolicy', {
    PolicyDocument: { Statement: Match.arrayWith([Match.objectLike({
      Effect: 'Deny', Action: 's3:*', Condition: { Bool: { 'aws:SecureTransport': 'false' } },
    })]) },
  });
  t.hasResource('AWS::S3::Bucket', { DeletionPolicy: 'Retain' });
  t.hasResource('AWS::DynamoDB::Table', { DeletionPolicy: 'Retain' });
});

test('reject unsafe tenant IDs before synthesis', () => {
  assert.throws(() => new EdgeStack(new App({ context: { tenants: ['../other'] } }), 'Unsafe'));
});
