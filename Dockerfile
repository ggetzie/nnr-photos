FROM public.ecr.aws/lambda/provided:al2 as build

# install go compiler
RUN yum install -y golang
RUN go env -w GOPROXY=direct

# adapted from:
#  https://github.com/libvips/libvips/issues/1844#issuecomment-706177342
#  https://github.com/jcupitt/docker-builds/blob/master/libvips-amazonlinux2/Dockerfile
# install libvips 

# install general build tools
ENV PKG_CONFIG_PATH=/usr/local/lib/pkgconfig
RUN yum update -y \
  && yum groupinstall -y "Development Tools" \
  && yum install -y wget nasm git

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
ARG WEBP_URL=https://storage.googleapis.com/downloads.webmproject.org/releases/webp/

RUN wget $WEBP_URL/libwebp-${WEBP_VERSION}.tar.gz \
  && tar xzf libwebp-${WEBP_VERSION}}.tar.gz \
  && cd libwebp-${WEBP_VERSION}} \
  && ./configure \
  && make V=0 \
  && make install

# install libde265 - required for libheif
ARG LIBDE265_URL=https://github.com/strukturag/libde265.git 
RUN git clone ${LIBDE265_URL} \
  && cd libde265 \
  && ./autogen.sh \
  && ./configure \
  && make V=0 \
  && make install

# install libheif  
ARG LIBHEIF_URL=https://github.com/strukturag/libheif.git
RUN git clone ${LIBHEIF_URL} \
  && cd libheif \
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
# build
ADD photos.go ./
RUN go build -o /main photos.go
# copy artifacts to a clean image
FROM public.ecr.aws/lambda/provided:al2
COPY --from=build /main /main
ENTRYPOINT [ "/main" ]           