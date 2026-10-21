import { App } from 'aws-cdk-lib';
import { EdgeStack } from '../lib/edge-stack';
const app = new App();
new EdgeStack(app, 'EdgePoc', {
  env: { account: process.env.CDK_DEFAULT_ACCOUNT, region: process.env.CDK_DEFAULT_REGION ?? 'ap-northeast-1' },
});
