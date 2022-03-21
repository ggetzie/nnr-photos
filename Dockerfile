FROM public.ecr.aws/lambda/provided:al2 as build

# install general build tools
ENV PKG_CONFIG_PATH=/usr/local/lib/pkgconfig
RUN yum update -y \
  && yum groupinstall -y "Development Tools" \
  && yum install -y wget nasm cmake

# install go compiler. Need newer version than is in the repository
ARG GO_VERSION=1.18
ARG GO_URL=https://go.dev/dl

RUN wget ${GO_URL}/go${GO_VERSION}.linux-amd64.tar.gz \
  && tar -C /usr/local -xzf go${GO_VERSION}.linux-amd64.tar.gz

ENV PATH=${PATH}:/usr/local/go/bin

RUN go env -w GOPROXY=direct
RUN go env -w GO111MODULE=auto

# install libvips 
# adapted from:
#  https://github.com/libvips/libvips/issues/1844#issuecomment-706177342
#  https://github.com/jcupitt/docker-builds/blob/master/libvips-amazonlinux2/Dockerfile

# selection of packages for libvips -- you might want to expand this
RUN yum install -y \
  expat-devel \
  glib2-devel \
  lcms2-devel \
  libexif-devel \
  libgsf-devel \
  libjpeg-turbo-devel \
  libpng-devel \
  libtiff-devel \
  giflib-devel \
  orc-devel 

# Latest version of libwebp in Amazon Linux repos is 0.3 need >= 0.5, compile from source
ARG WEBP_VERSION=1.2.2
ARG WEBP_URL=https://storage.googleapis.com/downloads.webmproject.org/releases/webp

RUN wget ${WEBP_URL}/libwebp-${WEBP_VERSION}.tar.gz \
  && tar xzf libwebp-${WEBP_VERSION}.tar.gz \
  && cd libwebp-${WEBP_VERSION} \
  && ./configure \
  && make V=0 \
  && make install

# install libde265 - required for libheif
ARG LIBDE265_VERSION=1.0.8
ARG LIBDE265_URL=https://github.com/strukturag/libde265/releases/download/v${LIBDE265_VERSION}/libde265-${LIBDE265_VERSION}.tar.gz
RUN wget ${LIBDE265_URL} \
  && tar xzf libde265-${LIBDE265_VERSION}.tar.gz \
  && cd libde265-${LIBDE265_VERSION} \
  && ./autogen.sh \
  && ./configure \
  && make V=0 \
  && make install

# install X265 - required for libheif
ARG X265_VERSION=3.5
ARG X265_URL=https://bitbucket.org/multicoreware/x265_git/downloads/x265_${X265_VERSION}.tar.gz

RUN wget ${X265_URL} \
  && tar xzf x265_${X265_VERSION}.tar.gz \
  && cd x265_${X265_VERSION} \
  && cmake source \
  && make V=0 \
  && make install


# install libheif  
ARG LIBHEIF_VERSION=1.12.0
ARG LIBHEIF_URL=https://github.com/strukturag/libheif/releases/download/v${LIBHEIF_VERSION}/libheif-${LIBHEIF_VERSION}.tar.gz
RUN wget ${LIBHEIF_URL} \
  && tar xzf libheif-${LIBHEIF_VERSION}.tar.gz \
  && cd  libheif-${LIBHEIF_VERSION} \
  && ./autogen.sh \
  && ./configure \
  && make V=0 \
  && make install


# compile libvips
ARG VIPS_VERSION=8.12.2
ARG VIPS_URL=https://github.com/libvips/libvips/releases/download  

RUN wget $VIPS_URL/v$VIPS_VERSION/vips-$VIPS_VERSION.tar.gz \
  && tar xzf vips-$VIPS_VERSION.tar.gz \
  && cd vips-$VIPS_VERSION \
  && ./configure \
  && make V=0 \
  && make install

# cache dependencies
ADD go.mod go.sum ./
RUN go mod download

# build, linking against libraries we installed
ADD photos.go ./
RUN go build -ldflags "-r /usr/local/lib" -o /main photos.go

# copy artifacts to a clean image
FROM public.ecr.aws/lambda/provided:al2
COPY --from=build /main /main
ENTRYPOINT [ "/main" ]           