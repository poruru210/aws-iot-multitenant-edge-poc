import * as path from 'node:path';
import { ArnFormat, CfnOutput, Duration, RemovalPolicy, Stack, type StackProps } from 'aws-cdk-lib';
import { Construct } from 'constructs';
import * as s3 from 'aws-cdk-lib/aws-s3';
import * as iam from 'aws-cdk-lib/aws-iam';
import * as iot from 'aws-cdk-lib/aws-iot';
import * as dynamodb from 'aws-cdk-lib/aws-dynamodb';
import * as lambda from 'aws-cdk-lib/aws-lambda';
import * as logs from 'aws-cdk-lib/aws-logs';
import * as apigateway from 'aws-cdk-lib/aws-apigateway';
import { NodejsFunction } from 'aws-cdk-lib/aws-lambda-nodejs';
import { validId } from '../functions/domain';

export class EdgeStack extends Stack {
  constructor(scope: Construct, id: string, props?: StackProps) {
    super(scope, id, props);
    const raw = this.node.tryGetContext('tenants') ?? ['tenant-a', 'tenant-b'];
    const tenants: string[] = typeof raw === 'string' ? JSON.parse(raw) : raw;
    if (!Array.isArray(tenants) || !tenants.length || tenants.length > 100 || !tenants.every(validId) || new Set(tenants).size !== tenants.length) throw new Error('tenants must contain 1-100 distinct safe identifiers');
    const project: string = this.node.tryGetContext('projectName') ?? 'edge-poc';
    if (!/^[a-z][a-z0-9-]{1,30}$/.test(project)) throw new Error('invalid projectName');
    const days = Number(this.node.tryGetContext('retentionDays') ?? 0);
    if (!Number.isInteger(days) || days < 0 || days > 3650) throw new Error('retentionDays must be 0-3650 (0 disables object expiration)');
    const templateName = `${project}-trusted-user`;
    const aliasName = `${project}-s3`;
    const policyName = `${project}-credential-policy`;
    const iotArn = (resource: string, name: string) => this.formatArn({ service: 'iot', resource, resourceName: name, arnFormat: ArnFormat.SLASH_RESOURCE_NAME });
    const templateArn = iotArn('provisioningtemplate', templateName);
    const aliasArn = iotArn('rolealias', aliasName);

    const bucket = new s3.Bucket(this, 'Data', {
      encryption: s3.BucketEncryption.S3_MANAGED,
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      enforceSSL: true, objectOwnership: s3.ObjectOwnership.BUCKET_OWNER_ENFORCED,
      removalPolicy: RemovalPolicy.RETAIN,
      lifecycleRules: [{ abortIncompleteMultipartUploadAfter: Duration.days(1), ...(days > 0 ? { expiration: Duration.days(days) } : {}) }],
    });
    const table = new dynamodb.Table(this, 'Ledger', {
      partitionKey: { name: 'pk', type: dynamodb.AttributeType.STRING },
      billingMode: dynamodb.BillingMode.PAY_PER_REQUEST,
      timeToLiveAttribute: 'ttl', removalPolicy: RemovalPolicy.RETAIN,
    });
    const types = tenants.map((tenant, index) => {
      const t = new iot.CfnThingType(this, `TenantType${index}`, { thingTypeName: tenant, thingTypeProperties: { thingTypeDescription: `Tenant ${tenant}; managed by ${project}` } });
      t.applyRemovalPolicy(RemovalPolicy.RETAIN);
      return t;
    });
    const uploadRole = new iam.Role(this, 'UploadRole', {
      assumedBy: new iam.ServicePrincipal('credentials.iot.amazonaws.com', {
        conditions: { StringEquals: { 'aws:SourceAccount': this.account }, ArnEquals: { 'aws:SourceArn': aliasArn } },
      }), maxSessionDuration: Duration.hours(1),
    });
    uploadRole.addToPolicy(new iam.PolicyStatement({
      actions: ['s3:PutObject', 's3:AbortMultipartUpload', 's3:ListMultipartUploadParts'],
      resources: [bucket.arnForObjects('tenants/${credentials-iot:ThingTypeName}/devices/${credentials-iot:ThingName}/*')],
      conditions: { Null: { 'credentials-iot:ThingTypeName': 'false', 'credentials-iot:ThingName': 'false' } },
    }));
    const alias = new iot.CfnRoleAlias(this, 'RoleAlias', { roleAlias: aliasName, roleArn: uploadRole.roleArn, credentialDurationSeconds: 900 });
    const policy = new iot.CfnPolicy(this, 'DevicePolicy', {
      policyName, policyDocument: { Version: '2012-10-17', Statement: [{ Effect: 'Allow', Action: 'iot:AssumeRoleWithCertificate', Resource: aliasArn }] },
    });
    policy.addDependency(alias);
    policy.applyRemovalPolicy(RemovalPolicy.RETAIN);
    const registrationRole = new iam.Role(this, 'RegistrationRole', {
      assumedBy: new iam.ServicePrincipal('iot.amazonaws.com', {
        conditions: { StringEquals: { 'aws:SourceAccount': this.account }, ArnEquals: { 'aws:SourceArn': templateArn } },
      }),
      // AWS's provisioning control-plane policy. It is NEVER attached to devices.
      managedPolicies: [iam.ManagedPolicy.fromAwsManagedPolicyName('service-role/AWSIoTThingsRegistration')],
    });
    const makeFunction = (name: string, file: string, timeout: number, environment: Record<string, string>) => {
      const logGroup = new logs.LogGroup(this, `${name}Logs`, { retention: logs.RetentionDays.TWO_WEEKS, removalPolicy: RemovalPolicy.RETAIN });
      return new NodejsFunction(this, name, {
        entry: path.join(__dirname, '../functions', file), handler: 'handler', runtime: lambda.Runtime.NODEJS_22_X,
        architecture: lambda.Architecture.ARM_64, memorySize: 256, timeout: Duration.seconds(timeout),
        bundling: { externalModules: [], minify: true, sourceMap: false },
        environment: { TABLE_NAME: table.tableName, ...environment }, logGroup,
      });
    };
    const hook = makeFunction('PreProvision', 'pre-provision.ts', 4, { TEMPLATE_ARN: templateArn });
    table.grant(hook, 'dynamodb:GetItem', 'dynamodb:UpdateItem');
    const permission = new lambda.CfnPermission(this, 'HookInvoke', {
      action: 'lambda:InvokeFunction', functionName: hook.functionName,
      principal: 'iot.amazonaws.com', sourceArn: templateArn, sourceAccount: this.account,
    });
    const template = new iot.CfnProvisioningTemplate(this, 'Template', {
      templateName, enabled: true, provisioningRoleArn: registrationRole.roleArn,
      preProvisioningHook: { payloadVersion: '2020-04-01', targetArn: hook.functionArn },
      templateBody: this.toJsonString({
        Parameters: { TenantId: { Type: 'String' }, ThingName: { Type: 'String' }, 'AWS::IoT::Certificate::Id': { Type: 'String' } },
        Resources: {
          thing: { Type: 'AWS::IoT::Thing', Properties: { ThingName: { Ref: 'ThingName' }, ThingTypeName: { Ref: 'TenantId' } }, OverrideSettings: { ThingTypeName: 'FAIL' } },
          certificate: { Type: 'AWS::IoT::Certificate', Properties: { CertificateId: { Ref: 'AWS::IoT::Certificate::Id' }, Status: 'ACTIVE', ThingPrincipalType: 'EXCLUSIVE_THING' } },
          policy: { Type: 'AWS::IoT::Policy', Properties: { PolicyName: policyName } },
        },
      }),
    });
    template.addDependency(permission);
    template.addDependency(policy);
    types.forEach(t => template.addDependency(t));
    const issue = makeFunction('IssueClaim', 'issue-claim.ts', 25, {
      TENANTS: JSON.stringify(tenants), TEMPLATE_NAME: templateName, ROLE_ALIAS: aliasName, BUCKET_NAME: bucket.bucketName,
    });
    table.grant(issue, 'dynamodb:PutItem', 'dynamodb:UpdateItem');
    issue.addToRolePolicy(new iam.PolicyStatement({ actions: ['iot:CreateProvisioningClaim'], resources: [templateArn] }));
    issue.addToRolePolicy(new iam.PolicyStatement({ actions: ['iot:DescribeEndpoint'], resources: ['*'] }));
    issue.addToRolePolicy(new iam.PolicyStatement({ actions: ['iot:DescribeThing'], resources: [iotArn('thing', '*')] }));
    const api = new apigateway.RestApi(this, 'RegistrationApi', {
      restApiName: `${project}-registration`, cloudWatchRole: false,
      deployOptions: { stageName: 'poc', dataTraceEnabled: false, throttlingRateLimit: 5, throttlingBurstLimit: 10 },
      defaultMethodOptions: { authorizationType: apigateway.AuthorizationType.IAM },
    });
    api.root.addResource('tenants').addResource('{tenantId}').addResource('claims')
      .addMethod('POST', new apigateway.LambdaIntegration(issue), { authorizationType: apigateway.AuthorizationType.IAM });
    tenants.forEach((tenant, index) => {
      const p = new iam.ManagedPolicy(this, `OperatorPolicy${index}`, { statements: [new iam.PolicyStatement({ actions: ['execute-api:Invoke'], resources: [api.arnForExecuteApi('POST', `/tenants/${tenant}/claims`, 'poc')] })] });
      new CfnOutput(this, `OperatorPolicy${index}Arn`, { value: p.managedPolicyArn, description: `Attach only to an operator authorized for ${tenant}` });
    });
    new CfnOutput(this, 'RegistrationApiUrl', { value: api.url });
    new CfnOutput(this, 'BucketName', { value: bucket.bucketName });
    new CfnOutput(this, 'LedgerTable', { value: table.tableName });
    new CfnOutput(this, 'TemplateName', { value: templateName });
    new CfnOutput(this, 'RoleAliasName', { value: aliasName });
  }
}
