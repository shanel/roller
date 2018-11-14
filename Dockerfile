FROM golang

RUN apt-get update -qqy && apt-get install -qqy \
    curl \
    gcc \
    python-dev \
    python-setuptools \
    apt-transport-https \
    lsb-release \
    openssh-client \
    git \
    gnupg \
    python-pip
RUN pip install -U crcmod

ENV GOPATH=/gocode/app

RUN echo "deb http://packages.cloud.google.com/apt cloud-sdk-stretch main" | tee -a /etc/apt/sources.list.d/google-cloud-sdk.list
RUN cat /etc/apt/sources.list.d/google-cloud-sdk.list
RUN curl https://packages.cloud.google.com/apt/doc/apt-key.gpg | apt-key add -
RUN apt-get update -qqy && apt-get install -qqy google-cloud-sdk
RUN apt-get install -qqy google-cloud-sdk-app-engine-python google-cloud-sdk-app-engine-go google-cloud-sdk-datastore-emulator

RUN mkdir -p /gocode/app/src
COPY ./project/*.* /gocode/app/project/

WORKDIR /gocode/app/project
RUN go get

CMD dev_appserver.py --log_level=debug --clear_datastore --port 8080 --host 0.0.0.0 --default_gcs_bucket_name dice-roller-174222.appspot.com app.yaml
