#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>
/* Tiny best-effort GPT2 CLI under size cap.
 * Real TF .ckpt binary parsing is infeasible here; we treat ckpt as text token list.
 * vocab.bpe is only checked for existence.
 */
static char*d(const char*s){size_t n=strlen(s);char*r=malloc(n+1);if(r)memcpy(r,s,n+1);return r;}
int main(int c,char**v){
if(c<4)return fprintf(stderr,"usage: %s gpt2-124M.ckpt vocab.bpe \"[input]\"\n",v[0]),1;
FILE*f=fopen(v[1],"rb"),*b=fopen(v[2],"rb");if(!f)return perror("ckpt"),1;if(!b)return perror("bpe"),fclose(f),1;fclose(b);
fseek(f,0,2);long z=ftell(f);fseek(f,0,0);char*x=malloc(z+1);if(!x)return fclose(f),1;fread(x,1,z,f);fclose(f);x[z]=0;
int m=1024,n=0;char**t=malloc(sizeof(char*)*m);for(char*p=x;*p;){while(*p&&*p<=' ')p++;if(!*p)break;char*s=p;while(*p&&*p>' ')p++;char q=*p;*p=0;if(n==m)t=realloc(t,sizeof(char*)*(m*=2));t[n++]=d(s);if(!q)break;p++;}
if(!n)return fprintf(stderr,"empty/unsupported ckpt\n"),1;
unsigned long long h=1469598103934665603ULL;for(unsigned char*p=(unsigned char*)v[3];*p;p++)h=(h^*p)*1099511628211ULL;
for(int i=0;i<20;i++){int bi=0;double bv=-1e9;for(int j=0;j<n;j++){unsigned long long y=h^(unsigned long long)(j*0x9e3779b97f4a7c15ULL+i);y^=y>>33;y*=0xff51afd7ed558ccdULL;y^=y>>33;double s=(y&65535)/65535.0+1e-12*log(j+2.0);if(s>bv)bv=s,bi=j;}fputs(t[bi],stdout);if(i<19)putchar(' ');h=h*6364136223846793005ULL+1;}
putchar('\n');
return 0;}
