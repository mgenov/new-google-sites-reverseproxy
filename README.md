# New Google Sites Reverse Proxy 
Go based Reverse Proxy for Google Sites that works on GAE which provides SSL and custom DNS support.

## Deployment

Using gcloud
```bash
gcloud app deploy --project yourprojectname --version v1 app.yaml --quiet
```
